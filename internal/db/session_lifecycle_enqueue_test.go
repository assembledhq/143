package db

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestLifecycleEnqueueOwnerBoundary(t *testing.T) {
	t.Parallel()
	tests := []string{"nil context", "nil transaction", "no owner budget", "cancelled owner", "different tenant"}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated transaction boundary probe")
			defer mock.Close()
			binding := lifecycleJobPayloadTestBinding(t, models.LifecycleDispatchMessage, true)
			orgID := binding.orgID
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			var tx pgx.Tx = mock
			switch name {
			case "nil context":
				ctx = nil
			case "nil transaction":
				tx = nil
			case "no owner budget":
				ctx = context.Background()
			case "cancelled owner":
				cancel()
			case "different tenant":
				orgID = uuid.New()
			}
			actual, err := enqueueLifecycleJobInTx(ctx, tx, orgID, binding, lifecycleEnqueueTestOptions(), nil)
			require.Error(t, err, "missing ownership, scope or budget must fail before database access")
			require.Equal(t, lifecycleEnqueueResult{}, actual, "boundary failure must return no candidate or enqueue disposition")
			require.NoError(t, mock.ExpectationsWereMet(), "enqueue cannot invent an owner transaction or query after cancellation")
		})
	}
}

func TestLifecycleEnqueueRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, wantText string
		change         func(*LifecycleJobBinding, *EnqueueOpts)
	}{
		{name: "zero job", change: func(b *LifecycleJobBinding, _ *EnqueueOpts) { b.jobID = uuid.Nil }},
		{name: "duplicate input", change: func(b *LifecycleJobBinding, _ *EnqueueOpts) { b.dispatchIDs[1] = b.dispatchIDs[0] }},
		{name: "input discovery bound", change: func(b *LifecycleJobBinding, _ *EnqueueOpts) {
			b.dispatchIDs = make([]uuid.UUID, defaultLifecycleConsumerLimits().structuredRows+1)
			for i := range b.dispatchIDs {
				b.dispatchIDs[i] = uuid.New()
			}
		}},
		{name: "controller is not input job", change: func(_ *LifecycleJobBinding, opts *EnqueueOpts) { opts.JobType = "synthetic_controller" }},
		{name: "wrong queue", change: func(_ *LifecycleJobBinding, opts *EnqueueOpts) { opts.Queue = "default" }},
		{name: "default run time cannot be a replay wildcard", wantText: "explicit run time and positive integer max attempts", change: func(_ *LifecycleJobBinding, opts *EnqueueOpts) { opts.RunAt = nil }},
		{name: "default attempts cannot be a replay wildcard", wantText: "explicit run time and positive integer max attempts", change: func(_ *LifecycleJobBinding, opts *EnqueueOpts) { opts.MaxAttempts = 0 }},
		{name: "negative attempts", wantText: "explicit run time and positive integer max attempts", change: func(_ *LifecycleJobBinding, opts *EnqueueOpts) { opts.MaxAttempts = -1 }},
		{name: "attempts outside PostgreSQL integer", wantText: "explicit run time and positive integer max attempts", change: func(_ *LifecycleJobBinding, opts *EnqueueOpts) { opts.MaxAttempts = 1 << 32 }},
		{name: "unencodable domain payload", change: func(_ *LifecycleJobBinding, opts *EnqueueOpts) { opts.Payload = make(chan bool) }},
		{name: "null domain envelope", change: func(_ *LifecycleJobBinding, opts *EnqueueOpts) { opts.Payload = json.RawMessage(`null`) }},
		{name: "ambiguous routing", change: func(_ *LifecycleJobBinding, opts *EnqueueOpts) {
			opts.Payload = json.RawMessage(`{"SESSION_ID":"22222222-2222-4222-8222-222222222222"}`)
		}},
		{name: "unbounded exponent", change: func(_ *LifecycleJobBinding, opts *EnqueueOpts) {
			opts.Payload = json.RawMessage(`{"domain_id":1e99999999999999999999999999}`)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated transaction expectations without a database or settlement owner")
			defer mock.Close()
			binding := lifecycleJobPayloadTestBinding(t, models.LifecycleDispatchMessage, true)
			opts := lifecycleEnqueueTestOptions()
			tt.change(&binding, &opts)
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			actual, err := enqueueLifecycleJobInTx(ctx, mock, binding.orgID, binding, opts, nil)
			require.Error(t, err, "invalid immutable input cannot reach transaction policy or insert SQL")
			if tt.wantText != "" {
				require.ErrorContains(t, err, tt.wantText, "invalid concrete options must fail validation, not merely receive an unexpected-query mock error")
			}
			require.Equal(t, lifecycleEnqueueResult{}, actual, "every invalid envelope must return zero rather than a partially usable disposition")
			require.NoError(t, mock.ExpectationsWereMet(), "validation must not begin, configure, query, insert, notify, retry or settle")
		})
	}
}

func TestLifecycleEnqueuePreservesTransactionErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("synthetic insert failure")
	tests := []struct {
		name, isolation string
		insertError     error
		wantText        string
	}{
		{name: "owner policy is not replaced", isolation: "repeatable read", wantText: "configured READ COMMITTED"},
		{name: "SQL error belongs to outer owner", isolation: "read committed", insertError: sentinel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create a supplied transaction that records every permitted query")
			defer mock.Close()
			binding := lifecycleJobPayloadTestBinding(t, models.LifecycleDispatchMessage, true)
			opts := lifecycleEnqueueTestOptions()
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			mock.ExpectQuery(regexp.QuoteMeta(lifecycleTransactionPolicyQuery)).WillReturnRows(pgxmock.NewRows([]string{"isolation", "marker", "lock", "statement"}).AddRow(tt.isolation, "1", "250ms", "1500ms"))
			if tt.insertError != nil {
				mock.ExpectQuery(regexp.QuoteMeta(`SELECT octet_length(($1::jsonb)::text)`)).WithArgs(pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"size"}).AddRow(500))
				mock.ExpectQuery("INSERT INTO jobs").WithArgs(anyArgs(9)...).WillReturnError(tt.insertError)
			}
			actual, err := enqueueLifecycleJobInTx(ctx, mock, binding.orgID, binding, opts, nil)
			if tt.insertError != nil {
				require.ErrorIs(t, err, sentinel, "real SQL failure must remain available to the outer unit's rollback/retry policy")
			} else {
				require.ErrorContains(t, err, tt.wantText, "incompatible supplied transactions must be rejected rather than replaced or reconfigured")
			}
			require.Equal(t, lifecycleEnqueueResult{}, actual, "transaction errors must not retain a successful or pending partial result")
			require.NoError(t, mock.ExpectationsWereMet(), "the enqueue helper must never begin, commit, roll back, retry or install settings")
		})
	}
}

func lifecycleEnqueueTestOptions() EnqueueOpts {
	runAt := time.Date(2030, 1, 2, 3, 4, 5, 123456000, time.UTC)
	return EnqueueOpts{Queue: "agent", JobType: "continue_session", Payload: json.RawMessage(`{}`), RunAt: &runAt, MaxAttempts: 3}
}

func TestLifecycleJobCanonicalExpansionBound(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, payload string
		wantError     bool
	}{
		{name: "opaque large integer retained", payload: `{"id":900719925474099312345}`},
		{name: "negative decimal exponent retained", payload: `{"id":-1.25e-10}`},
		{name: "bounded expanded number", payload: `{"id":1e100000}`},
		{name: "huge positive expansion", payload: `{"id":1e10000000}`, wantError: true},
		{name: "huge negative expansion", payload: `{"id":1e-10000000}`, wantError: true},
		{name: "aggregate expansion bounded", payload: `{"ids":[` + strings.TrimSuffix(strings.Repeat(`1e100000,`, 50), ",") + `]}`, wantError: true},
		{name: "composite separators count toward expansion", payload: `{"ids":[` + strings.Repeat(`[],`, 300000) + strings.TrimSuffix(strings.Repeat(`1e100000,`, 35), ",") + `]}`, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := boundLifecycleJobCanonicalExpansion([]byte(tt.payload))
			if tt.wantError {
				require.ErrorIs(t, err, ErrLifecycleConsumerDiscoveryIncomplete, "numeric expansion must be bounded before PostgreSQL renders or stores it")
				return
			}
			require.NoError(t, err, "resource preflight must preserve supported opaque numeric identities without float conversion")
		})
	}
}
