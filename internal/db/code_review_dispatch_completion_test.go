package db

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewDispatchCompletionInvalidInputs(t *testing.T) {
	t.Parallel()
	operations := []struct {
		name             string
		requiresMetadata bool
		call             func(context.Context, uuid.UUID, pgx.Tx, uuid.UUID, uuid.UUID) error
	}{
		{name: "completion prelock", call: func(ctx context.Context, org uuid.UUID, tx pgx.Tx, _ uuid.UUID, session uuid.UUID) error {
			return LockCodeReviewMetadataForCompletionInTx(ctx, org, tx, session)
		}},
		{name: "already completed reconciliation", requiresMetadata: true, call: ReconcileCompletedCodeReviewDispatchInTx},
	}
	tests := []struct {
		name                                    string
		nilContext, nilTx, typedNilTx, canceled bool
		nilOrg, nilMetadata, nilSession         bool
		expected                                error
	}{
		{name: "nil context", nilContext: true, expected: ErrCodeReviewMetadataTerminalCapability},
		{name: "nil transaction", nilTx: true, expected: ErrCodeReviewMetadataTerminalCapability},
		{name: "typed nil transaction", typedNilTx: true, expected: ErrCodeReviewMetadataTerminalCapability},
		{name: "already canceled context", canceled: true, expected: context.Canceled},
		{name: "nil org", nilOrg: true, expected: ErrCodeReviewDispatchCompletionInput},
		{name: "nil metadata", nilMetadata: true, expected: ErrCodeReviewDispatchCompletionInput},
		{name: "nil session", nilSession: true, expected: ErrCodeReviewDispatchCompletionInput},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			t.Parallel()
			for _, tt := range tests {
				if tt.nilMetadata && !operation.requiresMetadata {
					continue
				}
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					mock := newCodeReviewMetadataMock(t)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					if tt.nilContext {
						ctx = nil
					} else if tt.canceled {
						cancel()
					}
					var tx pgx.Tx = mock
					if tt.nilTx {
						tx = nil
					} else if tt.typedNilTx {
						var absent *pgxpool.Tx
						tx = absent
					}
					org, metadata, session := uuid.New(), uuid.New(), uuid.New()
					if tt.nilOrg {
						org = uuid.Nil
					}
					if tt.nilMetadata {
						metadata = uuid.Nil
					}
					if tt.nilSession {
						session = uuid.Nil
					}
					err := operation.call(ctx, org, tx, metadata, session)
					require.ErrorIs(t, err, tt.expected, "invalid explicit completion inputs must refuse before scope SQL or transaction control")
					expectedText := tt.expected.Error()
					if tt.canceled {
						expectedText = ErrCodeReviewMetadataTerminalStorage.Error()
					}
					require.EqualError(t, err, expectedText, "input refusal must expose only its fixed safe error while retaining the internal cause")
					require.NoError(t, mock.ExpectationsWereMet(), "invalid inputs must not query rows borrow a checkout or begin another transaction")
				})
			}
		})
	}
}

func TestLockCodeReviewMetadataForCompletionInTx(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, failure string
		expected      error
	}{
		{name: "all session metadata prelocked in deterministic order"},
		{name: "missing session owner", failure: "missing", expected: pgx.ErrNoRows},
		{name: "isolation refusal", failure: "isolation", expected: ErrCodeReviewMetadataTerminalIsolation},
		{name: "lock query failure", failure: "query", expected: ErrCodeReviewMetadataTerminalStorage},
		{name: "lock stream failure", failure: "stream", expected: ErrCodeReviewMetadataTerminalStorage},
		{name: "lock scan failure", failure: "scan", expected: ErrCodeReviewMetadataTerminalStorage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCodeReviewMetadataMock(t)
			org, session := uuid.New(), uuid.New()
			first := uuid.MustParse("00000000-0000-4000-8000-000000000001")
			second := uuid.MustParse("00000000-0000-4000-8000-000000000002")
			cause := errors.New("synthetic private completion lock fault")
			isolation := string(pgx.ReadCommitted)
			if tt.failure == "isolation" {
				isolation = string(pgx.RepeatableRead)
			}
			mock.ExpectQuery(`SHOW transaction_isolation`).WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow(isolation))
			if tt.failure != "isolation" {
				query := mock.ExpectQuery(codeReviewMetadataTerminalSessionLocks).WithArgs(pgx.NamedArgs{
					"org_id": org, "session_id": session, "active_only": false,
				})
				if tt.failure == "query" {
					query.WillReturnError(cause)
				} else {
					rows := pgxmock.NewRows([]string{"id"})
					switch tt.failure {
					case "missing":
					case "scan":
						rows.AddRow("synthetic invalid uuid")
					case "stream":
						rows.AddRow(first).AddRow(second).RowError(1, cause)
					default:
						rows.AddRow(first).AddRow(second)
					}
					query.WillReturnRows(rows).RowsWillBeClosed()
				}
			}
			err := LockCodeReviewMetadataForCompletionInTx(context.Background(), org, mock, session)
			if tt.expected == nil {
				require.NoError(t, err, "prelock must consume every exact session metadata row without mutating or settling anything")
			} else {
				require.ErrorIs(t, err, tt.expected, "prelock refusal must retain the actual scope isolation or storage outcome")
				require.EqualError(t, err, tt.expected.Error(), "prelock error must not expose raw scope or driver text")
			}
			if tt.failure == "query" || tt.failure == "stream" {
				require.ErrorIs(t, err, cause, "fixed safe prelock errors must retain the original internal failure cause")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "prelock must use exact org session and inactive-inclusive ordered locks with no mutation Begin commit or child work")
		})
	}
}

func TestReconcileCompletedCodeReviewDispatchInTx(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, failure string
		pendingRows   int64
		expected      error
	}{
		{name: "completed owner with supported pending rows", pendingRows: 2},
		{name: "completed owner without supported pending rows"},
		{name: "missing or noncompleted owner", failure: "missing", expected: pgx.ErrNoRows},
		{name: "isolation refusal", failure: "isolation", expected: ErrCodeReviewMetadataTerminalIsolation},
		{name: "parent query failure", failure: "query", expected: ErrCodeReviewMetadataTerminalStorage},
		{name: "parent row failure", failure: "stream", expected: ErrCodeReviewMetadataTerminalStorage},
		{name: "parent scan failure", failure: "scan", expected: ErrCodeReviewMetadataTerminalStorage},
		{name: "foreign actual org", failure: "org", expected: ErrCodeReviewMetadataTerminalStorage},
		{name: "different actual metadata", failure: "metadata", expected: ErrCodeReviewMetadataTerminalStorage},
		{name: "different actual session", failure: "session", expected: ErrCodeReviewMetadataTerminalStorage},
		{name: "later child statement failure", failure: "child", expected: ErrCodeReviewMetadataTerminalStorage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCodeReviewMetadataMock(t)
			org, metadata, session := uuid.New(), uuid.New(), uuid.New()
			cause := errors.New("synthetic private completed reconciliation fault")
			isolation := string(pgx.ReadCommitted)
			if tt.failure == "isolation" {
				isolation = string(pgx.Serializable)
			}
			mock.ExpectQuery(`SHOW transaction_isolation`).WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow(isolation))
			if tt.failure != "isolation" {
				query := mock.ExpectQuery(codeReviewDispatchCompletedMetadataLock).WithArgs(org, metadata, session)
				if tt.failure == "query" {
					query.WillReturnError(cause)
				} else {
					actualOrg, actualMetadata, actualSession := org, metadata, session
					switch tt.failure {
					case "org":
						actualOrg = uuid.New()
					case "metadata":
						actualMetadata = uuid.New()
					case "session":
						actualSession = uuid.New()
					}
					rows := pgxmock.NewRows([]string{"id", "org_id", "session_id"})
					switch tt.failure {
					case "missing":
					case "scan":
						rows.AddRow("synthetic invalid uuid", actualOrg, actualSession)
					case "stream":
						rows.AddRow(actualMetadata, actualOrg, actualSession).RowError(0, cause)
					default:
						rows.AddRow(actualMetadata, actualOrg, actualSession)
					}
					query.WillReturnRows(rows).RowsWillBeClosed()
				}
				if tt.failure == "" || tt.failure == "child" {
					child := mock.ExpectExec(codeReviewMetadataCancelPending).WithArgs(org, []uuid.UUID{metadata}, []uuid.UUID{session})
					if tt.failure == "child" {
						child.WillReturnError(cause)
					} else {
						child.WillReturnResult(pgxmock.NewResult("UPDATE", tt.pendingRows))
					}
				}
			}
			err := ReconcileCompletedCodeReviewDispatchInTx(context.Background(), org, mock, metadata, session)
			if tt.expected == nil {
				require.NoError(t, err, "completed reconciliation must issue only the later supported-pending cancellation after consuming its exact owner lock")
			} else {
				require.ErrorIs(t, err, tt.expected, "completed reconciliation must refuse a missing incompatible or unreadable exact owner")
				require.EqualError(t, err, tt.expected.Error(), "reconciliation refusal must not expose raw identifiers or storage error text")
			}
			if tt.failure == "query" || tt.failure == "stream" || tt.failure == "child" {
				require.ErrorIs(t, err, cause, "fixed reconciliation errors must preserve the original cause for internal diagnosis")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "reconciliation must preserve exact tenant owner binding and separate parent-child statement order without completion rewrites or transaction effects")
		})
	}
}
