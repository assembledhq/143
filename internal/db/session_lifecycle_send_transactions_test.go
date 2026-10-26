package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestNewThreadSendTransactions(t *testing.T) {
	t.Parallel()
	// These exact signatures cannot admit an already-owned connection or a
	// structural BeginTx implementation as the public transaction boundary.
	var _ func(*pgxpool.Pool) (*ThreadSendTransactions, error) = NewThreadSendTransactions
	var _ func(*ThreadSendTransactions, context.Context, func(context.Context, pgx.Tx) error) error = (*ThreadSendTransactions).Run
	tests := []struct {
		name    string
		nilPool bool
	}{
		{name: "concrete pool retained without acquisition"},
		{name: "nil pool rejected", nilPool: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// No pool is opened: construction may only retain this concrete
			// dependency and must never acquire, configure or probe a backend.
			var pool *pgxpool.Pool
			if !tt.nilPool {
				pool = &pgxpool.Pool{}
			}
			actual, err := NewThreadSendTransactions(pool)
			var expected *ThreadSendTransactions
			if tt.nilPool {
				require.EqualError(t, err, "thread send transactions: pool is required", "nil concrete pools must fail before creating a usable send transaction boundary")
			} else {
				require.NoError(t, err, "constructing the sealed runner must not start a transaction or contact a backend")
				expected = &ThreadSendTransactions{pool: pool}
				require.Same(t, pool, actual.pool, "construction must retain the exact owned pool pointer rather than copy pool state or substitute a starter")
			}
			require.Equal(t, expected, actual, "the runner must hold exactly the supplied concrete pool and no alternative starter or raw-connection fallback")
		})
	}
}

func TestThreadSendTransactionsRejectsBeforeAcquisition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, receiver, input, expectedError string
		cause                                error
	}{
		{name: "nil receiver", receiver: "nil", expectedError: "thread send transactions: pool is required"},
		{name: "typed nil receiver", receiver: "typed nil", expectedError: "thread send transactions: pool is required"},
		{name: "zero receiver", receiver: "zero", expectedError: "thread send transactions: pool is required"},
		{name: "nil context delegated", input: "nil context", expectedError: "run lifecycle transaction: nil context"},
		{name: "nil work delegated", input: "nil work", expectedError: "run lifecycle transaction: nil work"},
		{name: "canceled context delegated", input: "canceled", expectedError: "lifecycle transaction budget: context canceled", cause: context.Canceled},
		{name: "expired context delegated", input: "expired", expectedError: "lifecycle transaction budget: context deadline exceeded", cause: context.DeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := &pgxpool.Pool{}
			runner, err := NewThreadSendTransactions(pool)
			require.NoError(t, err, "create the concrete wrapper without opening a pool or network connection")
			switch tt.receiver {
			case "nil":
				runner = nil
			case "typed nil":
				var typedNil *ThreadSendTransactions
				runner = typedNil
			case "zero":
				runner = &ThreadSendTransactions{}
			}
			ctx := context.Background()
			calls := 0
			work := func(context.Context, pgx.Tx) error {
				calls++
				return errors.New("invalid runner inputs must not invoke work")
			}
			switch tt.input {
			case "nil context":
				ctx = nil
			case "nil work":
				work = nil
			case "canceled":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			case "expired":
				expired, cancel := context.WithDeadline(ctx, time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC))
				defer cancel()
				ctx = expired
			}
			actual := runner.Run(ctx, work)
			require.EqualError(t, actual, tt.expectedError, "nil boundary state must fail closed and configured wrappers must retain the underlying runner's exact pre-acquisition validation")
			if tt.cause != nil {
				require.ErrorIs(t, actual, tt.cause, "the shared transaction runner's cancellation and deadline identities must survive delegation")
			}
			require.Equal(t, 0, calls, "invalid and expired units must not run work, retry it or reach an uninitialized pool")
			if tt.receiver == "" {
				expected := RunLifecycleTransaction(ctx, pool, work)
				require.EqualError(t, actual, expected.Error(), "the sealed wrapper must delegate the exact existing lifecycle transaction policy rather than duplicate or replace it")
				require.Equal(t, 0, calls, "comparing the existing validation path must still require no network or callback execution")
			}
		})
	}
}
