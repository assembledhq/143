package main

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// This stub proves explicit ownership plumbing only. It never invokes work,
// acquires a connection or establishes commit, event or PostgreSQL behavior.
type metadataCompositionTransactions struct {
	calls int
	err   error
}

func (r *metadataCompositionTransactions) Run(context.Context, func(context.Context, pgx.Tx) error) error {
	r.calls++
	return r.err
}

func TestBuildSessionExecutorStoresCodeReviewTerminalComposition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		supplyStore      bool
		cancelBeforeCall bool
		expectedRunCalls int
	}{
		{
			name:             "fallback uses the existing sealed pool runner",
			cancelBeforeCall: true,
		},
		{
			name:             "supplied aware store retains its explicit runner",
			supplyStore:      true,
			expectedRunCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Inert constructor-only pool: the canceled fallback context must
			// stop RunLifecycleTransaction before BeginTx can acquire anything.
			pool := &pgxpool.Pool{}
			runnerRefusal := errors.New("synthetic terminal runner refusal")
			runner := &metadataCompositionTransactions{err: runnerRefusal}
			deps := sessionExecutorStoreDeps{Pool: pool}
			if tt.supplyStore {
				store, err := db.NewCodeReviewStoreWithTerminalTransactions(pool, runner)
				require.NoError(t, err, "supplied test store should have explicit terminal ownership without acquisition")
				deps.CodeReviews = store
			}

			stores, err := buildSessionExecutorStores(deps)
			require.NoError(t, err, "executor store construction should not acquire a database connection")
			require.NotNil(t, stores.CodeReviews, "executor should provide a code review store for either construction path")
			if tt.supplyStore {
				require.Same(t, deps.CodeReviews, stores.CodeReviews, "builder should retain the exact supplied aware store rather than replace its owner")
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelBeforeCall {
				cancel()
			}
			metadata, err := stores.CodeReviews.CancelReview(ctx, uuid.New(), uuid.New(), "synthetic composition check")
			expectedError := runnerRefusal
			if tt.cancelBeforeCall {
				expectedError = context.Canceled
			}
			require.ErrorIs(t, err, expectedError, "terminal cancellation should reach the selected explicit runner without invoking SQL")
			require.False(t, errors.Is(err, db.ErrCodeReviewMetadataTerminalCapability), "configured executor terminal stores should not refuse missing transaction ownership")
			require.Equal(t, models.CodeReviewSessionMetadata{}, metadata, "refused terminal work should expose no provisional metadata result")
			require.Equal(t, tt.expectedRunCalls, runner.calls, "only a supplied explicit test capability should receive exactly one Run call")
		})
	}
}
