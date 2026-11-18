package db

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// appendApprovedGrantHistoricalJoinedInTx deliberately retains the pre-fix SQL
// only as historical mixed-writer evidence. No production path calls this helper.
// Its expected lost update is a drain/floor limitation, never a desired contract
// or evidence that the repaired implementation loses a committed grant.
func appendApprovedGrantHistoricalJoinedInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID, item models.AgentCapabilitySnapshotItem) ([]models.AgentCapabilitySnapshotItem, error) {
	rows, err := tx.Query(ctx, approvedGrantReadSQL+"\nFOR UPDATE OF s", pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
	if err != nil {
		return nil, err
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[struct {
		Snapshot        []models.AgentCapabilitySnapshotItem
		AutomationRunID *uuid.UUID
	}])
	if err != nil {
		return nil, err
	}
	for _, existing := range row.Snapshot {
		if existing.ID == item.ID {
			return row.Snapshot, fmt.Errorf("%w: %q", ErrCapabilityAlreadyGranted, item.ID)
		}
	}
	snapshot := append(row.Snapshot, item)
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, approvedGrantSessionSQL, pgx.NamedArgs{"snapshot": string(raw), "org_id": orgID, "session_id": sessionID}); err != nil {
		return nil, err
	}
	if row.AutomationRunID != nil {
		if _, err := tx.Exec(ctx, approvedGrantAutomationSQL, pgx.NamedArgs{"snapshot": string(raw), "org_id": orgID, "run_id": *row.AutomationRunID}); err != nil {
			return nil, err
		}
	}
	return snapshot, nil
}

func runApprovedGrantUnsupportedIsolationPostgresCase(t *testing.T, database *pgxpool.Pool, isolation pgx.TxIsoLevel, wrapper bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owned := newAutomationReaperFixture(t, database)
	sessionID := owned.session(t, string(models.SessionStatusIdle), nil, nil, nil)
	fixture := newApprovedGrantTestFixture()
	raw, err := json.Marshal(fixture.existing)
	require.NoError(t, err, "encode exact original snapshots before attempting an unsupported transaction isolation")
	_, err = database.Exec(ctx, `INSERT INTO session_execution_metadata(session_id,org_id,capability_snapshot)
		VALUES($1,$2,$3::jsonb) ON CONFLICT(session_id) DO UPDATE SET capability_snapshot=EXCLUDED.capability_snapshot`, sessionID, owned.orgID, string(raw))
	require.NoError(t, err, "seed the original session snapshot independently of the rejected grant")
	_, err = database.Exec(ctx, "UPDATE automation_runs SET capability_snapshot=$3::jsonb WHERE org_id=$1 AND id=$2", owned.orgID, owned.runID, string(raw))
	require.NoError(t, err, "seed the linked automation snapshot independently of the rejected grant")
	config := database.Config()
	config.MaxConns, config.MinConns, config.MinIdleConns = 1, 0, 0
	if wrapper {
		config.ConnConfig.RuntimeParams["default_transaction_isolation"] = string(isolation)
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err, "open a single physical connection with the selected caller or wrapper isolation")
	defer pool.Close()
	var actual []models.AgentCapabilitySnapshotItem
	if wrapper {
		actual, err = NewAgentCapabilityPolicyStore(pool).AppendApprovedSessionGrant(ctx, owned.orgID, sessionID, fixture.item)
		require.ErrorIs(t, err, ErrCapabilityGrantRequiresReadCommitted, "the unchanged wrapper Begin must reject an unsupported inherited default rather than downgrade it")
		var after string
		require.NoError(t, pool.QueryRow(ctx, "SHOW transaction_isolation").Scan(&after), "the wrapper must release its rejected transaction without replacing connection defaults")
		require.Equal(t, string(isolation), after, "the wrapper must preserve the caller-configured transaction isolation after rejection")
	} else {
		tx, beginErr := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation})
		require.NoError(t, beginErr, "begin the caller-owned transaction with exactly its requested unsupported isolation")
		defer func() { _ = tx.Rollback(context.Background()) }()
		actual, err = NewAgentCapabilityPolicyStore(nil).AppendApprovedSessionGrantInTx(ctx, owned.orgID, tx, sessionID, fixture.item)
		require.ErrorIs(t, err, ErrCapabilityGrantRequiresReadCommitted, "caller-owned unsupported isolation must fail before grant locks or writes")
		var after string
		require.NoError(t, tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&after), "a rejected caller transaction must remain usable and unsettled")
		require.Equal(t, string(isolation), after, "the persistence seam must not silently downgrade caller isolation")
		var accessedParent bool
		require.NoError(t, database.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND relation='sessions'::regclass)`, tx.Conn().PgConn().PID()).Scan(&accessedParent), "inspect this exact caller backend before it accesses any session relation")
		require.False(t, accessedParent, "unsupported isolation must be rejected before even taking the parent relation lock")
		require.NoError(t, tx.Rollback(ctx), "only the caller may settle its rejected transaction")
	}
	require.Nil(t, actual, "unsupported isolation cannot return a staged snapshot that a caller might mistake for accepted permission")
	var sessionSnapshot, runSnapshot []models.AgentCapabilitySnapshotItem
	err = database.QueryRow(ctx, `SELECT sem.capability_snapshot,ar.capability_snapshot
		FROM session_execution_metadata sem JOIN automation_runs ar ON ar.org_id=sem.org_id AND ar.id=$3
		WHERE sem.org_id=$1 AND sem.session_id=$2`, owned.orgID, sessionID, owned.runID).Scan(&sessionSnapshot, &runSnapshot)
	require.NoError(t, err, "inspect both snapshots independently after the unsupported grant is rejected")
	requireApprovedGrantJSON(t, fixture.existing, sessionSnapshot, "isolation rejection must preserve the entire session capability snapshot")
	requireApprovedGrantJSON(t, fixture.existing, runSnapshot, "isolation rejection must preserve the entire linked automation capability snapshot")
	var writerVersion *string
	require.NoError(t, pool.QueryRow(ctx, "SELECT current_setting('lifecycle143.writer_version',true)").Scan(&writerVersion), "inspect the same physical connection after the nonmutating grant rejection")
	require.Nil(t, writerVersion, "grant freshness must not install a lifecycle protocol marker or claim compatibility authority")
}
