package worker

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// This narrow CPU owner runs the real setup, resolver, canonical scope guards,
// persistence and effects against independently expected SQL. It is not proof
// of PostgreSQL same-connection locking, durable commit or contention behavior.
type workerThreadSendSQLTransactions struct{ mock pgxmock.PgxPoolIface }

func (r workerThreadSendSQLTransactions) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	unitCtx, cancel := context.WithTimeout(ctx, db.LifecycleTransactionTimeout)
	defer cancel()
	tx, err := r.mock.Begin(unitCtx)
	if err != nil {
		return err
	}
	if err = db.ConfigureLifecycleTransactionInTx(unitCtx, tx); err == nil {
		err = work(unitCtx, tx)
	}
	if err == nil {
		err = unitCtx.Err()
	}
	if err != nil {
		if rollbackErr := tx.Rollback(unitCtx); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return tx.Commit(unitCtx)
}

func workerThreadSendSQLDependencies(t *testing.T, stores *Stores, mock pgxmock.PgxPoolIface) threadsvc.SendDependencies {
	t.Helper()
	deps, err := threadsvc.NewSendDependencies(&pgxpool.Pool{}, stores.Sessions, stores.SessionThreads, stores.Jobs)
	require.NoError(t, err, "wire the original worker persistence/effects stores without constructor SQL")
	deps.Transactions = workerThreadSendSQLTransactions{mock: mock}
	return deps
}

const workerThreadSendSettingsSQL = `SELECT
		set_config('lifecycle143.writer_version', '1', true),
		set_config('lock_timeout', '250ms', true),
		set_config('statement_timeout', '1500ms', true)`
const workerThreadSendPolicySQL = `SELECT
		current_setting('transaction_isolation'),
		COALESCE(current_setting('lifecycle143.writer_version', true), ''),
		current_setting('lock_timeout'),
		current_setting('statement_timeout')`
const workerThreadSendSharedSQL = `SELECT pg_try_advisory_xact_lock_shared(hashtextextended($1::text, 0))`
const workerThreadSendModeSQL = `SELECT s.org_id, s.id, s.lifecycle_version,
	c.org_id, c.session_id, c.version, c.state, c.legacy_input_cutoff_id,
	c.normalization, c.activated_at, c.minimum_writer_version, c.resumability_capability,
	s.agent_type, COALESCE((SELECT jsonb_agg(jsonb_build_object(
		'protocol_version', t.lifecycle_version, 'provider', t.agent_type) ORDER BY t.id)
		FROM session_threads t WHERE t.org_id = s.org_id AND t.session_id = s.id
		AND t.archived_at IS NULL), '[]'::jsonb)
	FROM sessions s LEFT JOIN session_lifecycle_cutovers c
	ON c.org_id = s.org_id AND c.session_id = s.id
	WHERE s.org_id = $1 AND s.id = $2`
const workerThreadSendParentSQL = `SELECT id, org_id, status, deleted_at, archived_at,
		admission_epoch, end_epoch, lifecycle_version
		FROM sessions WHERE org_id = $1 AND id = $2
		FOR NO KEY UPDATE NOWAIT`
const workerThreadSendKeysSQL = `SELECT id FROM session_threads
	WHERE org_id = $1 AND session_id = $2 AND (archived_at IS NULL OR id = $3)
	ORDER BY id LIMIT $4`
const workerThreadSendLanesSQL = `SELECT id, org_id, session_id, status, archived_at,
		admission_epoch, end_epoch, lifecycle_version
		FROM session_threads
		WHERE org_id = $1 AND session_id = $2 AND id = ANY($3::uuid[])
		ORDER BY id FOR NO KEY UPDATE NOWAIT`

func expectWorkerThreadSendSQLStart(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SHOW transaction_isolation")).WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow("read committed"))
	mock.ExpectExec(regexp.QuoteMeta(workerThreadSendSettingsSQL)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	expectWorkerThreadSendSQLPolicy(mock)
	mock.ExpectQuery(regexp.QuoteMeta(workerThreadSendSharedSQL)).WithArgs("session_lifecycle_cutover:" + orgID.String() + ":" + sessionID.String()).WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(workerThreadSendModeSQL)).WithArgs(orgID, sessionID).WillReturnRows(pgxmock.NewRows([]string{"org", "session", "protocol", "cutover_org", "cutover_session", "version", "state", "cutoff", "normalization", "activated", "minimum", "capability", "provider", "lanes"}).AddRow(orgID, sessionID, int16(0), nil, nil, nil, nil, nil, nil, nil, nil, nil, models.AgentTypeCodex, json.RawMessage(`[{"protocol_version":0,"provider":"codex"}]`)))
}

func expectWorkerThreadSendSQLPolicy(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery(regexp.QuoteMeta(workerThreadSendPolicySQL)).WillReturnRows(pgxmock.NewRows([]string{"isolation", "marker", "lock", "statement"}).AddRow("read committed", "1", "250ms", "1500ms"))
}

func expectWorkerThreadSendSQLEntry(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
	expectWorkerThreadSendSQLStart(mock, orgID, sessionID)
	mock.ExpectCommit()
}

// The caller adds all transaction-scoped deciding reads and send writes after
// these guards, followed by the whole send's single explicit completion.
func expectWorkerThreadSendSQLMutation(mock pgxmock.PgxPoolIface, orgID, sessionID, threadID uuid.UUID, parentStatus models.SessionStatus, laneStatus models.ThreadStatus) {
	expectWorkerThreadSendSQLStart(mock, orgID, sessionID)
	expectWorkerThreadSendSQLPolicy(mock)
	mock.ExpectQuery(regexp.QuoteMeta(workerThreadSendParentSQL)).WithArgs(orgID, sessionID).WillReturnRows(pgxmock.NewRows([]string{"id", "org", "status", "deleted", "archived", "epoch", "end", "protocol"}).AddRow(sessionID, orgID, parentStatus, nil, nil, int64(0), nil, int16(0)))
	mock.ExpectQuery(regexp.QuoteMeta(workerThreadSendKeysSQL)).WithArgs(orgID, sessionID, threadID, 4097).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(threadID))
	mock.ExpectQuery(regexp.QuoteMeta(workerThreadSendLanesSQL)).WithArgs(orgID, sessionID, []uuid.UUID{threadID}).WillReturnRows(pgxmock.NewRows([]string{"id", "org", "session", "status", "archived", "epoch", "end", "protocol"}).AddRow(threadID, orgID, sessionID, laneStatus, nil, int64(0), nil, int16(0)))
}
