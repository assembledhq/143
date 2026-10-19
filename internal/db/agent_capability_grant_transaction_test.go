package db

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

const approvedGrantLockSQL = `SELECT COALESCE(sem.capability_snapshot, '[]'::jsonb), sal.automation_run_id
	FROM sessions s
	LEFT JOIN session_execution_metadata sem
	  ON sem.org_id = s.org_id AND sem.session_id = s.id
	LEFT JOIN session_automation_links sal
	  ON sal.org_id = s.org_id AND sal.session_id = s.id
	WHERE s.org_id = @org_id AND s.id = @session_id AND s.deleted_at IS NULL
	FOR UPDATE OF s`

const approvedGrantSessionSQL = `INSERT INTO session_execution_metadata (session_id, org_id, capability_snapshot)
	VALUES (@session_id, @org_id, @snapshot::jsonb)
	ON CONFLICT (session_id) DO UPDATE
	SET capability_snapshot = EXCLUDED.capability_snapshot,
	    updated_at = now()`

const approvedGrantAutomationSQL = `UPDATE automation_runs
	SET capability_snapshot = @snapshot::jsonb
	WHERE org_id = @org_id AND id = @run_id`

type approvedGrantTestFixture struct {
	orgID, sessionID, runID uuid.UUID
	existing                []models.AgentCapabilitySnapshotItem
	item                    models.AgentCapabilitySnapshotItem
}

func newApprovedGrantTestFixture() approvedGrantTestFixture {
	requestID := uuid.New()
	return approvedGrantTestFixture{
		orgID: uuid.New(), sessionID: uuid.New(), runID: uuid.New(),
		existing: []models.AgentCapabilitySnapshotItem{{
			ID: models.AgentCapabilityRepoContext, DisplayName: "Repository context", AccessLevel: models.AgentCapabilityAccessRead,
			Risk: models.AgentCapabilityRiskLow, Scope: models.AgentCapabilityScopeRepository, Config: json.RawMessage(`{"scope":"synthetic"}`),
			Source: models.AgentCapabilityGrantSourceSessionDefault, GrantedAt: time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC),
		}},
		item: models.AgentCapabilitySnapshotItem{
			ID: models.AgentCapabilitySessionHistory, DisplayName: "Session history", AccessLevel: models.AgentCapabilityAccessRead,
			Risk: models.AgentCapabilityRiskLow, Scope: models.AgentCapabilityScopeOrg, Config: json.RawMessage(`{"max_age_days":7}`),
			Source: models.AgentCapabilityGrantSourceUserApproved, GrantedAt: time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC), HumanInputRequestID: &requestID,
		},
	}
}

type approvedGrantTestCase struct {
	name, failure, wantText string
	linked                  bool
	cause                   error
}

func approvedGrantPersistenceCases() []approvedGrantTestCase {
	return []approvedGrantTestCase{
		{name: "append standalone snapshot"},
		{name: "append linked automation snapshot", linked: true},
		{name: "duplicate returns unchanged snapshot", failure: "duplicate", cause: ErrCapabilityAlreadyGranted},
		{name: "foreign org or missing session returns no rows", failure: "missing", cause: pgx.ErrNoRows},
		{name: "session lock query error", failure: "lock", cause: io.EOF, wantText: "lock session capability snapshot"},
		{name: "invalid snapshot scan error", failure: "scan", wantText: "not supported"},
		{name: "new snapshot marshal error", failure: "marshal", wantText: "marshal capability snapshot"},
		{name: "session snapshot write error", failure: "session", cause: io.ErrUnexpectedEOF, wantText: "update session capability snapshot"},
		{name: "automation snapshot write error", failure: "automation", linked: true, cause: io.ErrClosedPipe, wantText: "update automation run capability snapshot"},
	}
}

func expectApprovedGrantPersistence(t *testing.T, mock pgxmock.PgxPoolIface, fixture *approvedGrantTestFixture, tt approvedGrantTestCase) []models.AgentCapabilitySnapshotItem {
	t.Helper()
	if tt.failure == "marshal" {
		fixture.item.Config = json.RawMessage(`invalid-json`)
	}
	if tt.failure == "duplicate" {
		fixture.item = fixture.existing[0]
	}
	query := mock.ExpectQuery(regexp.QuoteMeta(approvedGrantLockSQL)).
		WithArgs(pgx.NamedArgs{"org_id": fixture.orgID, "session_id": fixture.sessionID})
	if tt.failure == "lock" {
		query.WillReturnError(tt.cause)
		return nil
	}
	rows := pgxmock.NewRows([]string{"capability_snapshot", "automation_run_id"})
	if tt.failure == "missing" {
		query.WillReturnRows(rows)
		return nil
	}
	// pgxmock assigns Go values; real JSONB decoding is covered by PostgreSQL
	// tests rather than pretending this mock implements the JSONB codec.
	var snapshotValue any = fixture.existing
	if tt.failure == "scan" {
		snapshotValue = 42
	}
	var runID *uuid.UUID
	if tt.linked {
		runID = &fixture.runID
	}
	query.WillReturnRows(rows.AddRow(snapshotValue, runID))
	if tt.failure == "duplicate" {
		return fixture.existing
	}
	if tt.failure == "scan" || tt.failure == "marshal" {
		return nil
	}
	expected := append(append([]models.AgentCapabilitySnapshotItem{}, fixture.existing...), fixture.item)
	raw, err := json.Marshal(expected)
	require.NoError(t, err, "the complete expected grant snapshot should serialize deterministically")
	update := mock.ExpectExec(regexp.QuoteMeta(approvedGrantSessionSQL)).
		WithArgs(pgx.NamedArgs{"snapshot": string(raw), "org_id": fixture.orgID, "session_id": fixture.sessionID})
	if tt.failure == "session" {
		update.WillReturnError(tt.cause)
		return nil
	}
	update.WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if tt.linked {
		automation := mock.ExpectExec(regexp.QuoteMeta(approvedGrantAutomationSQL)).
			WithArgs(pgx.NamedArgs{"snapshot": string(raw), "org_id": fixture.orgID, "run_id": fixture.runID})
		if tt.failure == "automation" {
			automation.WillReturnError(tt.cause)
			return nil
		}
		automation.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	}
	return expected
}

func requireApprovedGrantResult(t *testing.T, expected, actual []models.AgentCapabilitySnapshotItem, err error, tt approvedGrantTestCase) {
	t.Helper()
	require.Equal(t, expected, actual, "the returned complete snapshot must preserve legacy append and duplicate behavior")
	if tt.cause != nil {
		require.ErrorIs(t, err, tt.cause, "the original persistence or duplicate cause must be preserved")
	} else if tt.failure == "" {
		require.NoError(t, err, "a valid grant should return its complete staged snapshot")
	} else {
		require.Error(t, err, "invalid snapshot data should not permit a partial grant")
	}
	if tt.wantText != "" {
		require.ErrorContains(t, err, tt.wantText, "existing error context must remain stable across extraction")
	}
}

func TestAgentCapabilityApprovedGrantLegacyTransaction(t *testing.T) {
	t.Parallel()
	tests := append(approvedGrantPersistenceCases(),
		approvedGrantTestCase{name: "begin failure never enters persistence", failure: "begin", cause: io.ErrNoProgress, wantText: "begin append capability grant tx"},
		approvedGrantTestCase{name: "commit failure returns no snapshot", failure: "commit", cause: io.EOF, wantText: "commit append capability grant tx"},
	)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newApprovedGrantTestFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "the legacy transaction mock should initialize")
			defer mock.Close()
			var expected []models.AgentCapabilitySnapshotItem
			begin := mock.ExpectBegin()
			if tt.failure == "begin" {
				begin.WillReturnError(tt.cause)
			} else {
				persistCase := tt
				if tt.failure == "commit" {
					persistCase = approvedGrantTestCase{}
				}
				expected = expectApprovedGrantPersistence(t, mock, &fixture, persistCase)
				if tt.failure == "" {
					mock.ExpectCommit()
				} else if tt.failure == "commit" {
					mock.ExpectCommit().WillReturnError(tt.cause)
					expected = nil
				}
				mock.ExpectRollback()
			}
			actual, err := NewAgentCapabilityPolicyStore(mock).AppendApprovedSessionGrant(context.Background(), fixture.orgID, fixture.sessionID, fixture.item)
			requireApprovedGrantResult(t, expected, actual, err, tt)
			require.NoError(t, mock.ExpectationsWereMet(), "the legacy wrapper must retain its exact Begin, persistence, Commit and deferred Rollback boundary")
		})
	}
}

func TestAgentCapabilityApprovedGrantInTxUsesCallerTransaction(t *testing.T) {
	t.Parallel()
	for _, tt := range approvedGrantPersistenceCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newApprovedGrantTestFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "the caller transaction mock should initialize")
			defer mock.Close()
			mock.ExpectBegin()
			tx, err := mock.Begin(context.Background())
			require.NoError(t, err, "only the test caller should begin the shared unit of work")
			expected := expectApprovedGrantPersistence(t, mock, &fixture, tt)
			store := NewAgentCapabilityPolicyStore(nil)
			actual, err := store.AppendApprovedSessionGrantInTx(context.Background(), fixture.orgID, tx, fixture.sessionID, fixture.item)
			requireApprovedGrantResult(t, expected, actual, err, tt)
			// SQL errors abort real PostgreSQL transactions. Only success and
			// application-level failures can continue without caller rollback.
			if tt.failure == "" || tt.failure == "duplicate" || tt.failure == "missing" || tt.failure == "marshal" {
				mock.ExpectQuery("^SELECT 42$").WillReturnRows(pgxmock.NewRows([]string{"value"}).AddRow(42))
				var value int
				require.NoError(t, tx.QueryRow(context.Background(), "SELECT 42").Scan(&value), "a non-aborted supplied transaction should remain available to its owner")
				require.Equal(t, 42, value, "the caller should be able to compose more database work before settling")
			}
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback(context.Background()), "only the caller should settle success or failure of the shared unit")
			require.NoError(t, mock.ExpectationsWereMet(), "InTx must use only the supplied transaction, with no pool access, nested transaction, commit, rollback or retry")
		})
	}
}

func TestAgentCapabilityApprovedGrantInTxRejectsNilTransaction(t *testing.T) {
	t.Parallel()
	var typedNil *lifecycleTransactionTestTx
	tests := []struct {
		name string
		tx   pgx.Tx
	}{
		{name: "nil interface"},
		{name: "typed nil transaction", tx: typedNil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newApprovedGrantTestFixture()
			actual, err := NewAgentCapabilityPolicyStore(nil).AppendApprovedSessionGrantInTx(context.Background(), fixture.orgID, tt.tx, fixture.sessionID, fixture.item)
			require.Nil(t, actual, "invalid transaction cannot produce a capability snapshot")
			require.ErrorContains(t, err, "nil transaction", "invalid transaction must return an error without falling back to the store pool")
		})
	}
}
