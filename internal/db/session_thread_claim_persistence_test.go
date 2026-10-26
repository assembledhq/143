package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type threadClaimPersistenceTx struct {
	pgx.Tx
	reader      DBTX
	queries     []threadPersistenceWrite
	contexts    []context.Context
	settlements []string
}

func (tx *threadClaimPersistenceTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.queries = append(tx.queries, threadPersistenceWrite{SQL: strings.Join(strings.Fields(sql), " "), Args: args})
	tx.contexts = append(tx.contexts, ctx)
	return tx.reader.Query(ctx, sql, args...)
}

func (tx *threadClaimPersistenceTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.queries = append(tx.queries, threadPersistenceWrite{SQL: strings.Join(strings.Fields(sql), " "), Args: args})
	tx.contexts = append(tx.contexts, ctx)
	return tx.reader.QueryRow(ctx, sql, args...)
}

func (tx *threadClaimPersistenceTx) Begin(context.Context) (pgx.Tx, error) {
	tx.settlements = append(tx.settlements, "begin")
	return nil, errors.New("inner begin is forbidden")
}

func (tx *threadClaimPersistenceTx) Commit(context.Context) error {
	tx.settlements = append(tx.settlements, "commit")
	return errors.New("inner commit is forbidden")
}

func (tx *threadClaimPersistenceTx) Rollback(context.Context) error {
	tx.settlements = append(tx.settlements, "rollback")
	return errors.New("inner rollback is forbidden")
}

func callThreadClaimInTx(ctx context.Context, store *SessionThreadStore, tx pgx.Tx, f threadPersistenceFixture, kind string, maxRunning int) (models.SessionThread, ThreadClaimPersistenceEffects, error) {
	if kind == "idle" {
		return store.ClaimIdleForSessionInTx(ctx, f.orgID, tx, f.sessionID, f.threadID, maxRunning)
	}
	return store.ClaimForResumeInSessionInTx(ctx, f.orgID, tx, f.sessionID, f.threadID, maxRunning)
}

func callThreadClaimLegacy(ctx context.Context, store *SessionThreadStore, f threadPersistenceFixture, kind string, maxRunning int) (models.SessionThread, error) {
	if kind == "idle" {
		return store.ClaimIdleForSession(ctx, f.orgID, f.sessionID, f.threadID, maxRunning)
	}
	return store.ClaimForResumeInSession(ctx, f.orgID, f.sessionID, f.threadID, maxRunning)
}

func expectedThreadClaimWrite(f threadPersistenceFixture, kind string, maxRunning int) threadPersistenceWrite {
	setClause := "started_at = now(),"
	statuses := []string{"idle"}
	if kind == "resume" {
		setClause += " completed_at = NULL,"
		statuses = []string{"completed", "failed", "cancelled", "awaiting_input"}
	}
	if maxRunning <= 0 {
		maxRunning = models.MaxRunningThreadsPerSession
	}
	query := `WITH locked_threads AS (
		SELECT id, status FROM session_threads
		WHERE org_id = @org_id AND session_id = @session_id AND archived_at IS NULL
		FOR UPDATE
	), target_claimable AS (
		SELECT 1 FROM locked_threads WHERE id = @id AND status = ANY(@claimable_statuses)
	), running_count AS (
		SELECT count(*) AS n FROM locked_threads WHERE id <> @id
		AND status IN ('pending', 'running', 'awaiting_input')
	), eligible AS (
		-- target is claimable AND adding it (siblings + 1) stays at-or-under
		-- max_running, i.e. siblings_active < max_running.
		SELECT 1 FROM target_claimable, running_count WHERE running_count.n < @max_running
	)
	UPDATE session_threads SET status = 'running', ` + setClause + `
	last_activity_at = now(), cancel_requested_at = NULL
	WHERE id = @id AND org_id = @org_id AND session_id = @session_id
	AND archived_at IS NULL AND EXISTS (SELECT 1 FROM eligible)
	RETURNING ` + sessionThreadSelectColumns
	return threadPersistenceWrite{
		SQL: strings.Join(strings.Fields(query), " "),
		Args: []any{pgx.NamedArgs{
			"id": f.threadID, "org_id": f.orgID, "session_id": f.sessionID,
			"max_running": maxRunning, "claimable_statuses": statuses,
		}},
	}
}

func expectedThreadClaimLimitLookup(f threadPersistenceFixture) threadPersistenceWrite {
	query := `SELECT
		COALESCE((SELECT status FROM session_threads WHERE id = @id AND org_id = @org_id AND archived_at IS NULL), '') AS target_status,
		(SELECT count(*) FROM session_threads
		WHERE org_id = @org_id AND session_id = @session_id AND id <> @id
		AND archived_at IS NULL
		AND status IN ('pending', 'running', 'awaiting_input')) AS sibling_active`
	return threadPersistenceWrite{
		SQL:  strings.Join(strings.Fields(query), " "),
		Args: []any{pgx.NamedArgs{"id": f.threadID, "org_id": f.orgID, "session_id": f.sessionID}},
	}
}

func threadClaimReturnedFixture(f threadPersistenceFixture) ([]any, models.SessionThread) {
	provider, explanation, category := "synthetic-original-provider", "synthetic-prior-explanation", "synthetic-prior-category"
	row := newSessionThreadRow(f.threadID, f.sessionID, f.orgID, "synthetic claim", f.at)
	row[9], row[10], row[11], row[12], row[15], row[16], row[17], row[18], row[25] = models.ThreadStatusRunning, &provider, 7, &f.at, &explanation, &category, &f.at, &f.at, 2
	return row, models.SessionThread{
		ID: f.threadID, SessionID: f.sessionID, OrgID: f.orgID,
		AgentType: models.AgentTypeClaudeCode, Label: "synthetic claim", Status: models.ThreadStatusRunning,
		AgentSessionID: &provider, CurrentTurn: 7, PendingMessageCount: 2,
		LastActivityAt: &f.at, StartedAt: &f.at, CompletedAt: &f.at,
		FailureExplanation: &explanation, FailureCategory: &category,
		CreatedAt: f.at, CreatedBySource: models.ThreadCreatedBySourceUser,
		RecoveryEventHistory: json.RawMessage("[]"),
		ExecutionMode:        models.ThreadExecutionModeWork, FilesystemMode: models.ThreadFilesystemModeReadWrite,
	}
}

func TestThreadClaimPersistenceInTx(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind, outcome string
		maxRunning    int
	}{}
	for _, kind := range []string{"idle", "resume"} {
		for _, outcome := range []string{"success", "query error", "cancelled", "not claimable", "at limit miss", "below limit miss", "diagnostic deferred", "scan failure"} {
			tests = append(tests, struct {
				kind, outcome string
				maxRunning    int
			}{kind, outcome, 2})
		}
		for _, cap := range []int{0, -1, 1, 9} {
			tests = append(tests, struct {
				kind, outcome string
				maxRunning    int
			}{kind, "success", cap})
		}
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.outcome+"/cap="+strconv.Itoa(tt.maxRunning), func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the supplied transaction's isolated mock connection")
			t.Cleanup(mock.Close)
			tx, unused := &threadClaimPersistenceTx{reader: mock}, &threadPersistenceTx{}
			store, redis := threadPersistenceStoreWithStreams(t, unused)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("synthetic claim failure")
			query := mock.ExpectQuery("WITH locked_threads").WithArgs(anyArgs(5)...)
			row, expected := threadClaimReturnedFixture(fixture)
			expectedQueries := []threadPersistenceWrite{expectedThreadClaimWrite(fixture, tt.kind, tt.maxRunning)}
			switch tt.outcome {
			case "success":
				query.WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns).AddRow(row...))
			case "query error":
				query.WillReturnError(failure)
			case "cancelled":
				cancel()
				query.WillReturnError(context.Canceled)
			case "scan failure":
				row[11] = "synthetic non-integer turn"
				query.WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns).AddRow(row...))
			default:
				query.WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns))
			}
			actual, effects, err := callThreadClaimInTx(ctx, store, tx, fixture, tt.kind, tt.maxRunning)
			if tt.outcome == "success" {
				require.NoError(t, err, "a supplied transaction must compose the complete legacy claim")
				require.Equal(t, expected, actual, "the exact returned row, not a second reload, is the claim result")
				require.Equal(t, ThreadClaimPersistenceEffects{runtime: models.NewThreadRuntimeEvent(expected), runtimeChanged: true}, effects, "successful claim effects must preserve the complete exact returned runtime snapshot")
			} else {
				require.Error(t, err, "failed or ineligible claims must reach the outer owner")
				require.Equal(t, models.SessionThread{}, actual, "failed claims must return no accepted thread")
				require.Equal(t, ThreadClaimPersistenceEffects{}, effects, "failed claims must return no publishable effects")
				switch tt.outcome {
				case "query error":
					require.EqualError(t, err, "claim session thread: synthetic claim failure", "retain the exact legacy query error wrapper")
					require.ErrorIs(t, err, failure, "retain the underlying database error identity")
				case "cancelled":
					require.ErrorIs(t, err, context.Canceled, "cancellation remains available to the outer owner")
				case "scan failure":
					require.NotErrorIs(t, err, pgx.ErrNoRows, "a row scan failure must not become an eligibility or cap lookup")
				default:
					require.Equal(t, pgx.ErrNoRows, err, "every InTx no-match must retain raw missing-row identity without diagnosing cap state while locks are retained")
				}
			}
			require.Equal(t, expectedQueries, tx.queries, "both modes retain every legacy status, tenancy, visibility, sibling-lock and cap predicate on the supplied transaction")
			for _, actualContext := range tx.contexts {
				require.Equal(t, ctx, actualContext, "the sole claim mutation retains the caller's exact context")
			}
			require.Empty(t, tx.settlements, "the helper must not start, commit or roll back the caller's transaction")
			require.Zero(t, unused.reads, "the mutation must not diagnose a miss through the receiver's unrelated pool")
			require.Empty(t, unused.writes, "a supplied-transaction claim may not execute pool writes")
			require.Empty(t, redis.Keys(), "enabled publishers must stay silent for every uncommitted success and failure")
			require.NoError(t, mock.ExpectationsWereMet(), "claim persistence must issue only the exact expected row operations")
		})
	}
}

func callThreadClaimLimitDiagnostic(ctx context.Context, store *SessionThreadStore, f threadPersistenceFixture, kind string, maxRunning int) (bool, error) {
	if kind == "idle" {
		return store.IsAtRunningLimitForIdleClaim(ctx, f.orgID, f.sessionID, f.threadID, maxRunning)
	}
	return store.IsAtRunningLimitForResumeClaim(ctx, f.orgID, f.sessionID, f.threadID, maxRunning)
}

func TestThreadClaimReadOnlyLimitDiagnostics(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name, kind, status, change string
		siblings, maxRunning       int
		expected                   bool
		wantError                  bool
	}
	tests := []testCase{}
	for _, kind := range []string{"idle", "resume"} {
		for _, base := range []testCase{
			{name: "at supported cap", status: "claimable", siblings: 3, maxRunning: 3, expected: true},
			{name: "above supported cap", status: "claimable", siblings: 4, maxRunning: 3, expected: true},
			{name: "below cap", status: "claimable", siblings: 2, maxRunning: 3},
			{name: "nonclaimable target", status: "running", siblings: 3, maxRunning: 3},
			{name: "missing target", siblings: 3, maxRunning: 3},
			{name: "zero defaults to three", status: "claimable", siblings: 2},
			{name: "zero reaches default cap", status: "claimable", siblings: 3, expected: true},
			{name: "negative defaults to three", status: "claimable", siblings: 3, maxRunning: -1, expected: true},
			{name: "explicit narrow cap", status: "claimable", siblings: 1, maxRunning: 1, expected: true},
			{name: "foreign organization arguments", change: "org", maxRunning: 3},
			{name: "different session arguments", status: "claimable", change: "session", maxRunning: 3},
			{name: "query error preserved", change: "query", maxRunning: 3, wantError: true},
			{name: "scan error preserved", status: "claimable", change: "scan", maxRunning: 3, wantError: true},
			{name: "cancellation preserved", change: "cancel", maxRunning: 3, wantError: true},
		} {
			base.name, base.kind = kind+"/"+base.name, kind
			if base.status == "claimable" {
				base.status = "idle"
				if kind == "resume" {
					base.status = "completed"
				}
			}
			tests = append(tests, base)
		}
	}
	for _, status := range []string{"failed", "cancelled", "awaiting_input"} {
		tests = append(tests, testCase{name: "resume/fixed status " + status, kind: "resume", status: status, siblings: 3, maxRunning: 3, expected: true})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			if tt.change == "org" {
				fixture.orgID = uuid.New()
			} else if tt.change == "session" {
				fixture.sessionID = uuid.New()
			}
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated current-state diagnostic connection")
			t.Cleanup(mock.Close)
			conn := &threadClaimPersistenceTx{reader: mock}
			store, redis := threadPersistenceStoreWithStreams(t, conn)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			statement := expectedThreadClaimLimitLookup(fixture)
			query := mock.ExpectQuery(regexp.QuoteMeta(statement.SQL)).WithArgs(statement.Args...)
			failure := errors.New("synthetic current-state lookup failure")
			switch tt.change {
			case "query":
				query.WillReturnError(failure)
			case "cancel":
				cancel()
				query.WillReturnError(context.Canceled)
			case "scan":
				query.WillReturnRows(pgxmock.NewRows([]string{"target_status", "sibling_active"}).AddRow(tt.status, "synthetic nonnumeric occupancy"))
			default:
				query.WillReturnRows(pgxmock.NewRows([]string{"target_status", "sibling_active"}).AddRow(tt.status, tt.siblings))
			}
			actual, err := callThreadClaimLimitDiagnostic(ctx, store, fixture, tt.kind, tt.maxRunning)
			if tt.wantError {
				require.Error(t, err, "a diagnostic failure must remain available for the caller to retain its original mutation miss")
				if tt.change == "query" {
					require.Equal(t, failure, err, "read-only diagnostics must preserve the exact underlying query failure")
				} else if tt.change == "cancel" {
					require.ErrorIs(t, err, context.Canceled, "read-only diagnostics must preserve caller cancellation")
				}
			} else {
				require.NoError(t, err, "a complete current-state read must not be mistaken for a claim or transaction operation")
			}
			require.Equal(t, tt.expected, actual, "only a target in the fixed mode-specific status set at the effective legacy cap is diagnostically limited")
			require.Equal(t, []threadPersistenceWrite{statement}, conn.queries, "the diagnostic must retain the one exact read-only legacy SQL statement and explicit tenant/session/thread arguments")
			require.Equal(t, []context.Context{ctx}, conn.contexts, "the current-state diagnostic must preserve the caller's exact context")
			require.Empty(t, conn.settlements, "a diagnostic cannot begin, commit, roll back or savepoint a transaction")
			require.Empty(t, redis.Keys(), "enabled Redis publishers must remain silent for all read-only diagnostic outcomes")
			require.NoError(t, mock.ExpectationsWereMet(), "a diagnostic must issue no claim mutation, lock query, retry or notification reload")
		})
	}
}

func TestThreadClaimLegacyMissingRowDiagnostics(t *testing.T) {
	t.Parallel()
	tests := []struct{ kind, outcome string }{}
	for _, kind := range []string{"idle", "resume"} {
		for _, outcome := range []string{"at cap", "below cap", "nonclaimable", "lookup failure", "scan failure"} {
			tests = append(tests, struct{ kind, outcome string }{kind, outcome})
		}
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.outcome, func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the legacy standalone statement handle")
			t.Cleanup(mock.Close)
			conn := &threadClaimPersistenceTx{reader: mock}
			store, redis := threadPersistenceStoreWithStreams(t, conn)
			mock.ExpectQuery("WITH locked_threads").WithArgs(anyArgs(5)...).WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns))
			lookup := mock.ExpectQuery(`SELECT\s+COALESCE`).WithArgs(anyArgs(3)...)
			status, siblings := "idle", 3
			if tt.kind == "resume" {
				status = "completed"
			}
			switch tt.outcome {
			case "lookup failure":
				lookup.WillReturnError(errors.New("synthetic best-effort lookup failure"))
			case "scan failure":
				lookup.WillReturnRows(pgxmock.NewRows([]string{"target_status", "sibling_active"}).AddRow(status, "synthetic nonnumeric occupancy"))
			default:
				if tt.outcome == "below cap" {
					siblings = 2
				} else if tt.outcome == "nonclaimable" {
					status = "running"
				}
				lookup.WillReturnRows(pgxmock.NewRows([]string{"target_status", "sibling_active"}).AddRow(status, siblings))
			}
			actual, err := callThreadClaimLegacy(context.Background(), store, fixture, tt.kind, 0)
			expectedErr := pgx.ErrNoRows
			if tt.outcome == "at cap" {
				expectedErr = ErrThreadRunningLimitReached
			}
			require.Equal(t, expectedErr, err, "legacy standalone wrappers must preserve post-statement cap mapping and otherwise retain the original no-row error")
			require.Equal(t, models.SessionThread{}, actual, "a legacy mutation miss must return no accepted runtime row")
			require.Equal(t, []threadPersistenceWrite{expectedThreadClaimWrite(fixture, tt.kind, 0), expectedThreadClaimLimitLookup(fixture)}, conn.queries, "legacy wrappers must still issue exactly the original mutation then best-effort diagnostic SQL")
			require.Empty(t, conn.settlements, "legacy standalone wrappers must not gain explicit transaction boundaries")
			require.Empty(t, redis.Keys(), "a legacy mutation miss must not publish a runtime event")
			require.NoError(t, mock.ExpectationsWereMet(), "legacy lookup failure must not cause retries or replace the original mutation contract")
		})
	}
}

func TestThreadClaimQueryErrorDoesNotDiagnose(t *testing.T) {
	t.Parallel()
	tests := []struct{ kind, adapter string }{
		{kind: "idle", adapter: "legacy"}, {kind: "resume", adapter: "legacy"},
		{kind: "idle", adapter: "explicit"}, {kind: "resume", adapter: "explicit"},
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.adapter, func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the isolated mutation-query failure handle")
			t.Cleanup(mock.Close)
			conn := &threadClaimPersistenceTx{reader: mock}
			store, redis := threadPersistenceStoreWithStreams(t, conn)
			mock.ExpectQuery("WITH locked_threads").WithArgs(anyArgs(5)...).WillReturnError(pgx.ErrNoRows)
			var actual models.SessionThread
			if tt.adapter == "legacy" {
				actual, err = callThreadClaimLegacy(context.Background(), store, fixture, tt.kind, 3)
			} else {
				var effects ThreadClaimPersistenceEffects
				actual, effects, err = callThreadClaimInTx(context.Background(), store, conn, fixture, tt.kind, 3)
				require.Equal(t, ThreadClaimPersistenceEffects{}, effects, "a query failure must not expose captured claim effects")
			}
			require.EqualError(t, err, "claim session thread: no rows in result set", "a query-stage error must retain its original wrapper rather than become a collected mutation miss")
			require.ErrorIs(t, err, pgx.ErrNoRows, "query error identity must remain available without triggering best-effort cap diagnosis")
			require.Equal(t, models.SessionThread{}, actual, "query failure must return no accepted row")
			require.Equal(t, []threadPersistenceWrite{expectedThreadClaimWrite(fixture, tt.kind, 3)}, conn.queries, "query-stage errors must never add a cap lookup in either adapter")
			require.Empty(t, conn.settlements, "query failure must not cause a transaction boundary or retry")
			require.Empty(t, redis.Keys(), "query failure must not publish a runtime event")
			require.NoError(t, mock.ExpectationsWereMet(), "only the exact original mutation query may execute on query-stage failure")
		})
	}
}

func TestThreadClaimWrappedNoRowOrigin(t *testing.T) {
	t.Parallel()
	tests := []struct{ kind, adapter, stage, diagnostic string }{}
	for _, kind := range []string{"idle", "resume"} {
		for _, base := range []struct{ adapter, stage, diagnostic string }{
			{adapter: "legacy", stage: "collection", diagnostic: "at cap"},
			{adapter: "legacy", stage: "collection", diagnostic: "below cap"},
			{adapter: "legacy", stage: "collection", diagnostic: "lookup failure"},
			{adapter: "explicit", stage: "collection"},
			{adapter: "legacy", stage: "query"},
			{adapter: "explicit", stage: "query"},
		} {
			tests = append(tests, struct{ kind, adapter, stage, diagnostic string }{kind, base.adapter, base.stage, base.diagnostic})
		}
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.adapter+"/"+tt.stage+"/"+tt.diagnostic, func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the isolated query-versus-collection failure handle")
			t.Cleanup(mock.Close)
			conn := &threadClaimPersistenceTx{reader: mock}
			store, redis := threadPersistenceStoreWithStreams(t, conn)
			wrapped := fmt.Errorf("synthetic wrapped no-row: %w", pgx.ErrNoRows)
			mutation := mock.ExpectQuery("WITH locked_threads").WithArgs(anyArgs(5)...)
			expectedQueries := []threadPersistenceWrite{expectedThreadClaimWrite(fixture, tt.kind, 3)}
			expectedErr := error(wrapped)
			if tt.stage == "query" {
				mutation.WillReturnError(wrapped)
				expectedErr = fmt.Errorf("claim session thread: %w", wrapped)
			} else {
				// pgx CollectOneRow returns rows.Err() unchanged after Next is
				// false. This injects a real collection-stage wrapped no-row,
				// distinct from an error returned by conn.Query itself.
				mutation.WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns).CloseError(wrapped)).RowsWillBeClosed()
			}
			if tt.adapter == "legacy" && tt.stage == "collection" {
				expectedQueries = append(expectedQueries, expectedThreadClaimLimitLookup(fixture))
				lookup := mock.ExpectQuery(`SELECT\s+COALESCE`).WithArgs(anyArgs(3)...)
				if tt.diagnostic == "lookup failure" {
					lookup.WillReturnError(errors.New("synthetic best-effort diagnostic failure"))
				} else {
					status, siblings := "idle", 3
					if tt.kind == "resume" {
						status = "completed"
					}
					if tt.diagnostic == "below cap" {
						siblings = 2
					} else {
						expectedErr = ErrThreadRunningLimitReached
					}
					lookup.WillReturnRows(pgxmock.NewRows([]string{"target_status", "sibling_active"}).AddRow(status, siblings))
				}
			}
			var actual models.SessionThread
			if tt.adapter == "legacy" {
				actual, err = callThreadClaimLegacy(context.Background(), store, fixture, tt.kind, 3)
			} else {
				var effects ThreadClaimPersistenceEffects
				actual, effects, err = callThreadClaimInTx(context.Background(), store, conn, fixture, tt.kind, 3)
				require.Equal(t, ThreadClaimPersistenceEffects{}, effects, "collection and query failures must return no deferred effects from InTx")
			}
			require.EqualError(t, err, expectedErr.Error(), "only legacy collection-stage errors.Is misses may run the original best-effort cap mapping")
			if expectedErr == ErrThreadRunningLimitReached {
				require.Equal(t, expectedErr, err, "legacy wrapped collection misses at cap must retain the exact cap sentinel")
			} else {
				require.ErrorIs(t, err, wrapped, "unmapped collection and query failures must preserve the exact original wrapped error identity")
				require.ErrorIs(t, err, pgx.ErrNoRows, "wrapped no-row identity must remain available to the owner without changing its failure origin")
			}
			require.Equal(t, models.SessionThread{}, actual, "failure at either mutation stage must expose no accepted row")
			require.Equal(t, expectedQueries, conn.queries, "legacy collection misses alone may diagnose; query-stage failures and all InTx outcomes must execute only the mutation")
			require.Empty(t, conn.settlements, "tracking failure origin must add no transaction boundary or retry")
			require.Empty(t, redis.Keys(), "no mutation failure or diagnostic may publish through enabled streams")
			require.NoError(t, mock.ExpectationsWereMet(), "the original error stage must control the exact legacy query count and rows must close on collection failure")
		})
	}
}

func TestThreadClaimPersistenceEffectsKeepReturnedSnapshot(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"idle", "resume"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create this isolated returned-row connection")
			t.Cleanup(mock.Close)
			row, _ := threadClaimReturnedFixture(fixture)
			_, expected := threadClaimReturnedFixture(fixture)
			mock.ExpectQuery("WITH locked_threads").WithArgs(anyArgs(5)...).WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns).AddRow(row...))
			unused := &threadPersistenceTx{}
			store, redis := threadPersistenceStoreWithStreams(t, unused)
			actual, effects, err := callThreadClaimInTx(context.Background(), store, &threadClaimPersistenceTx{reader: mock}, fixture, kind, 2)
			require.NoError(t, err, "capture the successful claim's independent notification data")
			actual.Status, actual.CurrentTurn, actual.PendingMessageCount = models.ThreadStatusFailed, 99, 99
			*actual.AgentSessionID, *actual.FailureExplanation, *actual.FailureCategory = "mutated provider", "mutated explanation", "mutated category"
			*actual.LastActivityAt, *actual.StartedAt, *actual.CompletedAt = fixture.at.Add(time.Hour), fixture.at.Add(2*time.Hour), fixture.at.Add(3*time.Hour)
			require.Empty(t, redis.Keys(), "even a successful claim and caller-side mutation must not publish before the owner asks")
			store.PublishCommittedThreadClaimEffects(context.Background(), fixture.orgID, effects)
			requireThreadPersistenceEvent(t, redis, models.NewThreadRuntimeEvent(expected))
			require.Zero(t, unused.reads, "claim effects publish their immutable returned snapshot without reloading a potentially later row")
			require.NoError(t, mock.ExpectationsWereMet(), "claim publication must not read through the supplied transaction")
		})
	}
}

func TestThreadClaimPersistenceLegacyPublication(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"idle", "resume"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create this legacy implicit-commit connection")
			t.Cleanup(mock.Close)
			row, expected := threadClaimReturnedFixture(fixture)
			mock.ExpectQuery("WITH locked_threads").WithArgs(anyArgs(5)...).WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns).AddRow(row...))
			conn := &threadClaimPersistenceTx{reader: mock}
			store, redis := threadPersistenceStoreWithStreams(t, conn)
			actual, err := callThreadClaimLegacy(context.Background(), store, fixture, kind, 0)
			require.NoError(t, err, "the existing wrapper must retain its successful implicit-statement contract")
			require.Equal(t, expected, actual, "the legacy wrapper must still return its exact claimed row")
			require.Equal(t, []threadPersistenceWrite{expectedThreadClaimWrite(fixture, kind, 0)}, conn.queries, "legacy wrappers must preserve the original SQL and default cap without a notification reload")
			require.Empty(t, conn.settlements, "extraction must not add explicit transaction boundaries to legacy wrappers")
			requireThreadPersistenceEvent(t, redis, models.NewThreadRuntimeEvent(expected))
			require.NoError(t, mock.ExpectationsWereMet(), "legacy returned-row publication must not add a pool query")
		})
	}
}

func TestThreadClaimPersistenceOwnerSettlement(t *testing.T) {
	t.Parallel()
	tests := []struct{ kind, settlement string }{}
	for _, kind := range []string{"idle", "resume"} {
		for _, settlement := range []string{"commit", "rollback"} {
			tests = append(tests, struct{ kind, settlement string }{kind, settlement})
		}
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.settlement, func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the outer transaction owner's isolated mock connection")
			t.Cleanup(mock.Close)
			mock.ExpectBegin()
			owner, err := mock.Begin(context.Background())
			require.NoError(t, err, "only the outer owner begins this test's composed unit")
			row, expected := threadClaimReturnedFixture(fixture)
			mock.ExpectQuery("WITH locked_threads").WithArgs(anyArgs(5)...).WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns).AddRow(row...))
			unused := &threadPersistenceTx{}
			store, redis := threadPersistenceStoreWithStreams(t, unused)
			_, effects, err := callThreadClaimInTx(context.Background(), store, owner, fixture, tt.kind, 2)
			require.NoError(t, err, "the helper must leave its successful write in the outer owner's transaction")
			require.Empty(t, redis.Keys(), "enabled notification streams must stay silent until the owner settles and explicitly publishes")
			if tt.settlement == "rollback" {
				mock.ExpectRollback()
				require.NoError(t, owner.Rollback(context.Background()), "the owner may roll back an otherwise successful claim as part of a larger failed unit")
				require.Empty(t, redis.Keys(), "discarding claim effects on rollback must not advertise accepted state")
			} else {
				mock.ExpectCommit()
				require.NoError(t, owner.Commit(context.Background()), "only the outer owner may commit the complete unit")
				require.Empty(t, redis.Keys(), "confirmed database commit alone must not send an in-memory claim effect")
				store.PublishCommittedThreadClaimEffects(context.Background(), fixture.orgID, effects)
				requireThreadPersistenceEvent(t, redis, models.NewThreadRuntimeEvent(expected))
			}
			require.Zero(t, unused.reads, "neither transaction persistence nor returned-row publication may borrow the receiver's pool")
			require.NoError(t, mock.ExpectationsWereMet(), "the helper must not settle the transaction or introduce any unowned operation")
		})
	}
}

func TestThreadClaimPersistenceEffectsPublicationRefusal(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"zero effects", "foreign organization", "streams disabled", "publish failure"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			_, snapshot := threadClaimReturnedFixture(fixture)
			unused := &threadPersistenceTx{}
			store, redis := threadPersistenceStoreWithStreams(t, unused)
			var logs bytes.Buffer
			store.SetLogger(zerolog.New(&logs))
			effects := ThreadClaimPersistenceEffects{runtime: models.NewThreadRuntimeEvent(snapshot), runtimeChanged: true}
			orgID := fixture.orgID
			switch outcome {
			case "zero effects":
				effects = ThreadClaimPersistenceEffects{}
			case "foreign organization":
				orgID = uuid.New()
			case "streams disabled":
				store.SetStreams(nil)
			case "publish failure":
				redis.SetError("synthetic publisher failure")
			}
			store.PublishCommittedThreadClaimEffects(context.Background(), orgID, effects)
			require.Empty(t, redis.Keys(), "refused or failed effects must not appear in any tenant stream")
			require.Zero(t, unused.reads, "snapshot effects must not reload even on refusal and publication failure")
			if outcome == "foreign organization" || outcome == "publish failure" {
				var actual map[string]any
				require.NoError(t, json.Unmarshal(logs.Bytes(), &actual), "notification refusal and best-effort failure must remain observable")
				if outcome == "foreign organization" {
					require.Equal(t, map[string]any{"level": "error", "org_id": orgID.String(), "effects_org_id": fixture.orgID.String(), "message": "refusing to publish thread claim effects for another organization"}, actual, "tenant mismatch must be refused before any publisher access")
				} else {
					require.Equal(t, "warn", actual["level"], "best-effort publication failure must warn without rejecting committed data")
				}
			} else {
				require.Empty(t, logs.String(), "zero effects and disabled publishers must remain no-ops")
			}
		})
	}
}

func TestThreadClaimPersistenceRejectsNilTransaction(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"idle", "resume", "pending"} {
		for _, typedNil := range []bool{false, true} {
			t.Run(kind+"/"+map[bool]string{false: "nil", true: "typed nil"}[typedNil], func(t *testing.T) {
				t.Parallel()
				fixture := newThreadPersistenceFixture()
				unused := &threadPersistenceTx{}
				store, redis := threadPersistenceStoreWithStreams(t, unused)
				var tx pgx.Tx
				if typedNil {
					var nilTx *threadClaimPersistenceTx
					tx = nilTx
				}
				if kind == "pending" {
					effects, err := store.IncrementPendingMessagesInTx(context.Background(), fixture.orgID, tx, fixture.threadID)
					require.EqualError(t, err, "persist pending thread message: transaction is required", "nil transactions must reject before the pending write")
					require.Equal(t, ThreadPendingMessagePersistenceEffects{}, effects, "rejected pending writes must return zero effects")
				} else {
					thread, effects, err := callThreadClaimInTx(context.Background(), store, tx, fixture, kind, 2)
					require.EqualError(t, err, "persist thread claim: transaction is required", "nil transactions must reject before any claim or lookup")
					require.Equal(t, models.SessionThread{}, thread, "missing transaction must return no claimed row")
					require.Equal(t, ThreadClaimPersistenceEffects{}, effects, "missing transaction must return no publishable claim effects")
				}
				require.Zero(t, unused.reads, "missing transaction must not fall back to the receiver's database")
				require.Empty(t, unused.writes, "missing transaction must not fall back to a pool write")
				require.Empty(t, redis.Keys(), "validation rejection must not publish through configured streams")
			})
		}
	}
}

func TestThreadPendingMessagePersistenceInTx(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"success", "missing row", "write error", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tx, unused := &threadPersistenceTx{rows: 1}, &threadPersistenceTx{}
			failure := errors.New("synthetic pending increment failure")
			switch outcome {
			case "missing row":
				tx.rows = 0
			case "write error":
				tx.writeErr = failure
			case "cancelled":
				cancel()
				tx.writeErr = context.Canceled
			}
			store, redis := threadPersistenceStoreWithStreams(t, unused)
			effects, err := store.IncrementPendingMessagesInTx(ctx, fixture.orgID, tx, fixture.threadID)
			switch outcome {
			case "success":
				require.NoError(t, err, "the legacy pending counter must compose in the supplied transaction")
				require.Equal(t, ThreadPendingMessagePersistenceEffects{orgID: fixture.orgID, threadID: fixture.threadID, inboxQueued: true}, effects, "pending effects retain only the exact current-row reload identity")
			case "missing row":
				require.Equal(t, pgx.ErrNoRows, err, "archived or missing rows retain the exact legacy missing-row sentinel")
			case "write error":
				require.Equal(t, failure, err, "write failures retain their original identity")
			case "cancelled":
				require.Equal(t, context.Canceled, err, "cancelled writes retain their original classification")
			}
			if outcome != "success" {
				require.Equal(t, ThreadPendingMessagePersistenceEffects{}, effects, "failed pending writes return no publishable identity")
			}
			require.Equal(t, []threadPersistenceWrite{{SQL: "UPDATE session_threads SET pending_message_count = pending_message_count + 1 WHERE id = @id AND org_id = @org_id AND archived_at IS NULL", Args: []any{pgx.NamedArgs{"id": fixture.threadID, "org_id": fixture.orgID}}}}, tx.writes, "the pending-count split retains every original visibility, tenancy and increment predicate")
			require.Equal(t, []context.Context{ctx}, tx.contexts, "the supplied context must reach the exact pending write")
			require.Empty(t, tx.settlements, "only the outer owner may begin, commit or roll back pending work")
			require.Zero(t, unused.reads, "pending persistence must not reload through the receiver pool before commit")
			require.Empty(t, unused.writes, "pending persistence must use only the supplied transaction")
			require.Empty(t, redis.Keys(), "configured publishers must remain silent for uncommitted pending writes")
		})
	}
}

func TestThreadPendingMessagePersistencePublication(t *testing.T) {
	t.Parallel()
	tests := []struct{ adapter, outcome string }{}
	for _, adapter := range []string{"explicit", "legacy"} {
		for _, outcome := range []string{"success", "reload failure", "publish failure", "streams disabled"} {
			tests = append(tests, struct{ adapter, outcome string }{adapter, outcome})
		}
	}
	for _, outcome := range []string{"zero effects", "foreign organization"} {
		tests = append(tests, struct{ adapter, outcome string }{"explicit", outcome})
	}
	for _, tt := range tests {
		t.Run(tt.adapter+"/"+tt.outcome, func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the post-commit pending-event reload handle")
			t.Cleanup(mock.Close)
			database := &threadPersistenceTx{reader: mock, rows: 1}
			store, redis := threadPersistenceStoreWithStreams(t, database)
			var logs bytes.Buffer
			store.SetLogger(zerolog.New(&logs))
			orgID := fixture.orgID
			effects := ThreadPendingMessagePersistenceEffects{orgID: fixture.orgID, threadID: fixture.threadID, inboxQueued: true}
			switch tt.outcome {
			case "zero effects":
				effects = ThreadPendingMessagePersistenceEffects{}
			case "foreign organization":
				orgID = uuid.New()
			case "streams disabled":
				store.SetStreams(nil)
			default:
				query := mock.ExpectQuery("SELECT").WithArgs(fixture.threadID, fixture.orgID)
				if tt.outcome == "reload failure" {
					query.WillReturnError(errors.New("synthetic later-row failure"))
				} else {
					row := newSessionThreadRow(fixture.threadID, fixture.sessionID, fixture.orgID, "synthetic later row", fixture.at)
					row[25] = 19
					query.WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns).AddRow(row...))
					if tt.outcome == "publish failure" {
						redis.SetError("synthetic pending publisher failure")
					}
				}
			}
			if tt.adapter == "legacy" {
				require.NoError(t, store.IncrementPendingMessages(context.Background(), fixture.orgID, fixture.threadID), "legacy pending increment remains successful despite best-effort notification failure")
				require.Equal(t, []threadPersistenceWrite{{SQL: "UPDATE session_threads SET pending_message_count = pending_message_count + 1 WHERE id = @id AND org_id = @org_id AND archived_at IS NULL", Args: []any{pgx.NamedArgs{"id": fixture.threadID, "org_id": fixture.orgID}}}}, database.writes, "legacy increment still executes exactly the original mutation before its notification reload")
			} else {
				store.PublishCommittedThreadPendingMessageEffects(context.Background(), orgID, effects)
				require.Empty(t, database.writes, "an explicit notification adapter must not write the pending counter again")
			}
			if tt.outcome == "success" {
				entries, err := redis.Stream("143:stream:{ses:" + fixture.sessionID.String() + "}:events")
				require.NoError(t, err, "the existing session stream receives the committed pending notification")
				var actual []models.SessionStreamEvent
				for _, entry := range entries {
					var event models.SessionStreamEvent
					require.NoError(t, json.Unmarshal([]byte(entry.Values[1]), &event), "decode the complete pending notification")
					actual = append(actual, event)
				}
				want := models.SessionStreamEvent{
					Type: models.SessionStreamEventThreadInboxQueued, SessionID: fixture.sessionID, OrgID: fixture.orgID,
					Data: map[string]any{"session_id": fixture.sessionID.String(), "thread_id": fixture.threadID.String(), "org_id": fixture.orgID.String(), "pending_message_count": float64(19)},
				}
				require.Equal(t, []models.SessionStreamEvent{want}, actual, "pending events intentionally reload the later visible count rather than preserving a stale increment-time snapshot")
			} else {
				require.Empty(t, redis.Keys(), "rejected or failed pending events must not create a stream event")
			}
			if tt.outcome == "foreign organization" || tt.outcome == "reload failure" || tt.outcome == "publish failure" {
				var record map[string]any
				require.NoError(t, json.Unmarshal(logs.Bytes(), &record), "refusal and best-effort notification failures remain visible in structured logs")
				if tt.outcome == "foreign organization" {
					require.Equal(t, map[string]any{"level": "error", "org_id": orgID.String(), "effects_org_id": fixture.orgID.String(), "message": "refusing to publish pending thread message effects for another organization"}, record, "a foreign pending effect is refused before reloading another tenant's row")
				} else {
					require.Equal(t, "warn", record["level"], "pending notification failures must warn without altering committed data")
				}
			} else {
				require.Empty(t, logs.String(), "successful or intentionally absent notifications must not emit spurious failures")
			}
			require.Empty(t, database.settlements, "post-commit effects and legacy increments must not introduce a transaction boundary")
			require.NoError(t, mock.ExpectationsWereMet(), "the pending adapter may perform only the exact expected tenant-scoped current-row reload")
		})
	}
}
