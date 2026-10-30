package db

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type codeReviewMetadataTestRun struct {
	run func(context.Context, func(context.Context, pgx.Tx) error) error
}

func (r *codeReviewMetadataTestRun) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	return r.run(ctx, work)
}

func newCodeReviewMetadataMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	require.NoError(t, err, "create a fresh explicit transaction mock for only this CPU case")
	t.Cleanup(mock.Close)
	return mock
}

func TestCodeReviewMetadataTerminalConstruction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		make func(pgxmock.PgxPoolIface) (*CodeReviewStore, error)
	}{
		{name: "missing runner", make: func(mock pgxmock.PgxPoolIface) (*CodeReviewStore, error) {
			return NewCodeReviewStoreWithTerminalTransactions(mock, nil)
		}},
		{name: "typed nil runner", make: func(mock pgxmock.PgxPoolIface) (*CodeReviewStore, error) {
			var run *codeReviewMetadataTestRun
			return NewCodeReviewStoreWithTerminalTransactions(mock, run)
		}},
		{name: "missing database", make: func(_ pgxmock.PgxPoolIface) (*CodeReviewStore, error) {
			return NewCodeReviewStoreWithTerminalTransactions(nil, &codeReviewMetadataTestRun{})
		}},
		{name: "typed nil database", make: func(_ pgxmock.PgxPoolIface) (*CodeReviewStore, error) {
			var pool *pgxpool.Pool
			return NewCodeReviewStoreWithTerminalTransactions(pool, &codeReviewMetadataTestRun{})
		}},
		{name: "missing supplied transaction", make: func(_ pgxmock.PgxPoolIface) (*CodeReviewStore, error) { return NewCodeReviewStoreInTx(nil) }},
		{name: "typed nil supplied transaction", make: func(_ pgxmock.PgxPoolIface) (*CodeReviewStore, error) {
			var tx *pgxpool.Tx
			return NewCodeReviewStoreInTx(tx)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCodeReviewMetadataMock(t)
			store, err := tt.make(mock)
			require.ErrorIs(t, err, ErrCodeReviewMetadataTerminalCapability, "construction must refuse missing capabilities without probing rows or casting DBTX")
			require.Nil(t, store, "failed construction must not return a usable terminal wrapper")
			require.NoError(t, mock.ExpectationsWereMet(), "capability construction must not issue SQL or acquire a connection")
		})
	}
}

func TestUnconfiguredCodeReviewStoreTerminalMethodsRefuse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call func(*CodeReviewStore, uuid.UUID, uuid.UUID) error
	}{
		{name: "batch stale", call: func(s *CodeReviewStore, o, id uuid.UUID) error {
			_, err := s.MarkStaleForPullRequestExceptHead(context.Background(), o, id, "synthetic-head", nil)
			return err
		}},
		{name: "stale", call: func(s *CodeReviewStore, o, id uuid.UUID) error {
			_, err := s.MarkStale(context.Background(), o, id, "synthetic reason")
			return err
		}},
		{name: "active stale", call: func(s *CodeReviewStore, o, id uuid.UUID) error {
			_, err := s.MarkActiveReviewStale(context.Background(), o, id, "synthetic reason")
			return err
		}},
		{name: "completion", call: func(s *CodeReviewStore, o, id uuid.UUID) error {
			_, err := s.CompleteReview(context.Background(), o, CompleteCodeReviewParams{SessionID: id, Decision: models.CodeReviewDecisionApproved})
			return err
		}},
		{name: "failure delegate", call: func(s *CodeReviewStore, o, id uuid.UUID) error {
			_, err := s.FailReview(context.Background(), o, id, "synthetic reason")
			return err
		}},
		{name: "failure status", call: func(s *CodeReviewStore, o, id uuid.UUID) error {
			_, err := s.FailReviewWithStatus(context.Background(), o, FailCodeReviewParams{SessionID: id, Code: models.CodeReviewStatusCodeWorkerFailed})
			return err
		}},
		{name: "cancellation", call: func(s *CodeReviewStore, o, id uuid.UUID) error {
			_, err := s.CancelReview(context.Background(), o, id, "synthetic reason")
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCodeReviewMetadataMock(t)
			err := tt.call(NewCodeReviewStore(mock), uuid.New(), uuid.New())
			require.ErrorIs(t, err, ErrCodeReviewMetadataTerminalCapability, "DBTX alone must never imply a runner supplied transaction or no-intent fallback")
			require.NoError(t, mock.ExpectationsWereMet(), "an unconfigured terminal wrapper must refuse before any no-intent probe or metadata SQL")
		})
	}
}

func TestCodeReviewMetadataRunDiscardsProvisionalResults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		replay, finalError bool
	}{
		{name: "known commit"},
		{name: "replay discards earlier rows", replay: true},
		{name: "unknown or failed settlement", finalError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCodeReviewMetadataMock(t)
			settlementErr := errors.New("synthetic final settlement uncertainty")
			run := &codeReviewMetadataTestRun{run: func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
				if err := work(ctx, mock); err != nil {
					return err
				}
				if tt.replay {
					if err := work(ctx, mock); err != nil {
						return err
					}
				}
				if tt.finalError {
					return settlementErr
				}
				return nil
			}}
			store, err := NewCodeReviewStoreWithTerminalTransactions(mock, run)
			require.NoError(t, err, "supply one explicit test Run capability without deriving it from DBTX")
			org := uuid.New()
			expected := []models.CodeReviewSessionMetadata{{ID: uuid.New(), OrgID: org, SessionID: uuid.New(), Status: models.CodeReviewSessionStatusStale}}
			calls := 0
			actual, err := store.terminalMetadata(context.Background(), org, true, func(_ context.Context, tx pgx.Tx) ([]models.CodeReviewSessionMetadata, error) {
				calls++
				require.Equal(t, mock, tx, "each attempt must use only the transaction supplied by Run")
				if calls > 1 {
					return nil, nil
				}
				return expected, nil
			})
			if tt.finalError {
				require.ErrorIs(t, err, settlementErr, "a final failure must retain its actual outer settlement outcome")
				require.Nil(t, actual, "provisional metadata must not escape a final error or unknown commit")
			} else {
				require.NoError(t, err, "only known nil Run settlement may expose its final callback result")
				if tt.replay {
					require.Nil(t, actual, "a successful empty replay must not leak metadata captured by an earlier attempt")
				} else {
					require.Equal(t, expected, actual, "a known commit may return its exact final changed rows")
				}
			}
			expectedCalls := 1
			if tt.replay {
				expectedCalls = 2
			}
			require.Equal(t, expectedCalls, calls, "the wrapper must neither add another retry nor skip the injected Run callback")
			require.NoError(t, mock.ExpectationsWereMet(), "wrapper ownership must add no Begin commit checkout query or fallback")
		})
	}
}

func TestCodeReviewMetadataExplicitInTxDoesNotRun(t *testing.T) {
	t.Parallel()
	mock := newCodeReviewMetadataMock(t)
	store, err := NewCodeReviewStoreInTx(mock)
	require.NoError(t, err, "explicit supplied-tx composition must accept only its named transaction")
	org := uuid.New()
	expected := []models.CodeReviewSessionMetadata{{ID: uuid.New(), OrgID: org, SessionID: uuid.New()}}
	actual, err := store.terminalMetadata(context.Background(), org, false, func(_ context.Context, tx pgx.Tx) ([]models.CodeReviewSessionMetadata, error) {
		require.Equal(t, mock, tx, "tx-bound work must stay on the exact supplied transaction")
		return expected, nil
	})
	require.NoError(t, err, "supplied-tx composition must leave outer settlement to its caller")
	require.Equal(t, expected, actual, "supplied-tx rows remain provisional descriptors for the outer owner")
	require.NoError(t, mock.ExpectationsWereMet(), "supplied-tx composition must not issue transaction control or borrow another checkout")
}

func TestCodeReviewMetadataIsolationRefusalPrecedesWrites(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, isolation string }{
		{name: "repeatable read", isolation: string(pgx.RepeatableRead)},
		{name: "serializable", isolation: string(pgx.Serializable)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCodeReviewMetadataMock(t)
			mock.ExpectQuery(`SHOW transaction_isolation`).WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow(tt.isolation))
			actual, err := CancelCodeReviewInTx(context.Background(), uuid.New(), mock, uuid.New(), "synthetic reason")
			require.ErrorIs(t, err, ErrCodeReviewMetadataTerminalIsolation, "an isolation level that cannot refresh the later statement snapshot must refuse before metadata locks or writes")
			require.Equal(t, models.CodeReviewSessionMetadata{}, actual, "isolation refusal must not return provisional metadata")
			require.NoError(t, mock.ExpectationsWereMet(), "observing isolation must not configure or retime the caller's transaction")
		})
	}
}
