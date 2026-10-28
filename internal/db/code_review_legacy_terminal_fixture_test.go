package db

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// These CPU fixtures model the explicitly supplied Run boundary and its known
// mock commit. They do not establish real pool ownership, transaction budgets,
// PostgreSQL snapshot visibility, lock scheduling, or commit acknowledgment.
type codeReviewTerminalCPUContextKey struct{}

func newCodeReviewTerminalCPUStore(t *testing.T, mock pgxmock.PgxPoolIface, beforeCommit func()) *CodeReviewStore {
	t.Helper()
	mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	calls, committed := 0, false
	t.Cleanup(func() {
		require.Equal(t, 1, calls, "the positive fixture must execute exactly one explicitly injected Run unit")
		require.True(t, committed, "the positive fixture must reach its expected known mock commit")
	})
	run := &codeReviewMetadataTestRun{run: func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
		calls++
		require.Equal(t, 1, calls, "the store must not introduce another Run attempt")
		unitCtx := context.WithValue(ctx, codeReviewTerminalCPUContextKey{}, true)
		tx, err := mock.BeginTx(unitCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			return err
		}
		unit := &codeReviewTerminalCPUTransaction{Tx: tx, t: t, ctx: unitCtx}
		if err := work(unitCtx, unit); err != nil {
			return errors.Join(err, unit.Rollback(unitCtx))
		}
		if beforeCommit != nil {
			beforeCommit()
		}
		if err := unit.Commit(unitCtx); err != nil {
			return err
		}
		committed = true
		return nil
	}}
	store, err := NewCodeReviewStoreWithTerminalTransactions(mock, run)
	require.NoError(t, err, "legacy positive fixtures must explicitly supply their terminal Run capability")
	return store
}

type codeReviewTerminalCPUTransaction struct {
	pgx.Tx
	t              *testing.T
	ctx            context.Context
	rows           *codeReviewTerminalCPURows
	isolationReads int
	rowQueries     int
	cancelled      bool
}

func (tx *codeReviewTerminalCPUTransaction) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	require.Same(tx.t, tx.ctx, ctx, "isolation must be observed in the supplied Run unit context")
	require.Equal(tx.t, 0, tx.rowQueries, "READ COMMITTED must be observed before owner locks or metadata mutation")
	tx.isolationReads++
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func (tx *codeReviewTerminalCPUTransaction) requireConsumedRows() {
	tx.t.Helper()
	require.NotNil(tx.t, tx.rows, "the terminal unit must have queried its expected row stream")
	require.True(tx.t, tx.rows.consumed, "every owner or updated metadata row must be consumed before the next statement")
	require.True(tx.t, tx.rows.closed, "the preceding row stream must be closed before the next statement")
}

func (tx *codeReviewTerminalCPUTransaction) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	require.Same(tx.t, tx.ctx, ctx, "owner locks and metadata mutation must use the supplied Run unit context")
	require.Equal(tx.t, 1, tx.isolationReads, "the unit must observe READ COMMITTED exactly once before locking owners")
	if tx.rowQueries > 0 {
		tx.requireConsumedRows()
	}
	tx.rowQueries++
	require.LessOrEqual(tx.t, tx.rowQueries, 2, "the terminal fixture must execute only its ordered owner lock and metadata UPDATE streams")
	rows, err := tx.Tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	tx.rows = &codeReviewTerminalCPURows{Rows: rows}
	return tx.rows, nil
}

func (tx *codeReviewTerminalCPUTransaction) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	require.Same(tx.t, tx.ctx, ctx, "pending-only cancellation must remain inside the supplied Run unit context")
	require.Equal(tx.t, 2, tx.rowQueries, "child cancellation must be a later statement after owner locks and metadata UPDATE")
	tx.requireConsumedRows()
	require.Positive(tx.t, tx.rows.count, "an empty metadata UPDATE must not issue child cancellation")
	require.False(tx.t, tx.cancelled, "changed owners must scope one set-based pending-only cancellation")
	tag, err := tx.Tx.Exec(ctx, sql, args...)
	if err == nil {
		tx.cancelled = true
	}
	return tag, err
}

func (tx *codeReviewTerminalCPUTransaction) Commit(ctx context.Context) error {
	require.Same(tx.t, tx.ctx, ctx, "the test Run owner must commit only its supplied unit")
	require.Equal(tx.t, 2, tx.rowQueries, "known mock commit must follow both expected row streams")
	tx.requireConsumedRows()
	require.Equal(tx.t, tx.rows.count > 0, tx.cancelled, "only actually returned metadata rows require pending-only cancellation before commit")
	return tx.Tx.Commit(ctx)
}

type codeReviewTerminalCPURows struct {
	pgx.Rows
	count            int
	consumed, closed bool
}

func (rows *codeReviewTerminalCPURows) Next() bool {
	if rows.Rows.Next() {
		rows.count++
		return true
	}
	rows.consumed = true
	return false
}

func (rows *codeReviewTerminalCPURows) Close() {
	rows.closed = true
	rows.Rows.Close()
}

func expectCodeReviewTerminalCPUOwners(mock pgxmock.PgxPoolIface, sql string, args pgx.NamedArgs, ids []uuid.UUID) {
	mock.ExpectQuery(`SHOW transaction_isolation`).WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow(string(pgx.ReadCommitted))).RowsWillBeClosed()
	rows := pgxmock.NewRows([]string{"id"})
	for _, id := range ids {
		rows.AddRow(id)
	}
	mock.ExpectQuery(sql).WithArgs(args).WillReturnRows(rows).RowsWillBeClosed()
}

func codeReviewTerminalCPUMetadataRows(metadata ...models.CodeReviewSessionMetadata) *pgxmock.Rows {
	rows := codeReviewMetadataRowsForTest()
	for _, m := range metadata {
		rows.AddRow(m.ID, m.OrgID, m.SessionID, m.RepositoryID, m.PullRequestID, m.PolicyID,
			m.BaseSHA, m.HeadSHA, m.FromFork, m.TriggerSource, m.Status, m.Phase, m.StatusCode, m.StatusMessage,
			m.RetryAt, m.LastErrorAt, m.RetryableFailure, m.Decision, m.Acceptable, m.Stale, m.SupersededBySessionID,
			m.ReviewOutputKey, m.PromptRecordKey, m.GitHubReviewID, m.GitHubReviewURL, m.FinalReviewBody,
			m.FailureReason, m.CompletedAt, m.CreatedAt)
	}
	return rows
}

func codeReviewTerminalCPUStaleMetadata(orgID, pullRequestID, sessionID uuid.UUID, headSHA string, now time.Time) models.CodeReviewSessionMetadata {
	return models.CodeReviewSessionMetadata{
		ID: uuid.New(), OrgID: orgID, SessionID: sessionID, RepositoryID: uuid.New(), PullRequestID: pullRequestID, PolicyID: uuid.New(),
		BaseSHA: "base", HeadSHA: headSHA, TriggerSource: models.CodeReviewTriggerSourceAppReviewer,
		Status: models.CodeReviewSessionStatusStale, Stale: true, ReviewOutputKey: "output-" + sessionID.String(), CompletedAt: &now, CreatedAt: now,
	}
}

func codeReviewTerminalCPUOrderedMetadataIDs(metadata []models.CodeReviewSessionMetadata) []uuid.UUID {
	ids := make([]uuid.UUID, len(metadata))
	for i, m := range metadata {
		ids[i] = m.ID
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return ids
}

func requireCodeReviewTerminalCPUNoEvent(t *testing.T, events <-chan models.CodeReviewUpdatedEvent) {
	t.Helper()
	require.Never(t, func() bool {
		select {
		case <-events:
			return true
		default:
			return false
		}
	}, 50*time.Millisecond, 5*time.Millisecond, "the terminal fixture must not publish before known mock commit or publish an extra event")
}

// Literal predicates keep the legacy fixtures sensitive to scope and ordering
// regressions independently of the production query constants.
const codeReviewTerminalCPUSessionLocks = `SELECT id FROM code_review_session_metadata
 WHERE org_id=@org_id AND session_id=@session_id
 AND (NOT @active_only OR status IN ('queued','running')) ORDER BY id FOR UPDATE`

const codeReviewTerminalCPUBatchLocks = `SELECT id FROM code_review_session_metadata
 WHERE org_id=@org_id AND pull_request_id=@pull_request_id AND head_sha<>@current_head_sha
 AND status IN ('queued','running') ORDER BY id FOR UPDATE`

const codeReviewTerminalCPUCancelPending = `WITH pending AS MATERIALIZED (
 SELECT r.id FROM code_review_agent_results r
 WHERE r.org_id=$1 AND r.role='reviewer' AND r.dispatch_version=1 AND r.dispatch_state='pending'
 AND EXISTS (
 SELECT 1 FROM unnest($2::uuid[],$3::uuid[]) AS changed(metadata_id,session_id)
 JOIN code_review_session_metadata m ON m.org_id=$1 AND m.id=changed.metadata_id AND m.session_id=changed.session_id
 WHERE r.session_id=changed.session_id
 ) ORDER BY r.id FOR UPDATE OF r
)
UPDATE code_review_agent_results r
 SET dispatch_state='cancelled',dispatch_resolution='owner_cancelled',dispatch_due_at=NULL,
 dispatch_writer_seq=r.dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp()
 FROM pending p WHERE r.org_id=$1 AND r.id=p.id AND r.role='reviewer'
 AND r.dispatch_version=1 AND r.dispatch_state='pending'`

const codeReviewTerminalCPUComplete = `UPDATE code_review_session_metadata
 SET status = 'completed', phase = NULL, decision = @decision, acceptable = @acceptable,
 github_review_id = @github_review_id, github_review_url = @github_review_url, final_review_body = @final_review_body,
 additions = COALESCE(@additions, additions), deletions = COALESCE(@deletions, deletions), risk_reason_details = @risk_reason_details,
 failure_reason = NULL, status_code = NULL, status_message = NULL, retry_at = NULL, last_error_at = NULL,
 retryable_failure = false, completed_at = now()
 WHERE org_id = @org_id AND session_id = @session_id AND id = ANY(@terminal_metadata_ids)
 RETURNING ` + codeReviewMetadataColumns

const codeReviewTerminalCPUStale = `UPDATE code_review_session_metadata
 SET status = 'stale', stale = true, phase = NULL, status_code = NULL, status_message = NULL,
 retry_at = NULL, last_error_at = NULL, retryable_failure = false,
 superseded_by_session_id = COALESCE(@superseded_by_session_id, superseded_by_session_id),
 completed_at = COALESCE(completed_at, now())
 WHERE org_id = @org_id AND pull_request_id = @pull_request_id AND head_sha <> @current_head_sha
 AND status IN ('queued', 'running') AND id = ANY(@terminal_metadata_ids)
 RETURNING ` + codeReviewMetadataColumns
