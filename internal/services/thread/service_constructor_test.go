package thread

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type unexpectedSendTransactions struct{ t *testing.T }

func (r *unexpectedSendTransactions) Run(context.Context, func(context.Context, pgx.Tx) error) error {
	r.t.Helper()
	r.t.Fatal("constructor-only fixture unexpectedly invoked the lifecycle transaction runner")
	return errors.New("unexpected lifecycle transaction runner")
}

// Constructor-only and unrelated method tests grant no mode/row authority and
// fail if send orchestration reaches this runner. Behavioral send tests install
// explicit guard-SQL fixtures after construction instead.
func requiredTestSendDependencies(t *testing.T) SendDependencies {
	t.Helper()
	deps, err := NewSendDependencies(&pgxpool.Pool{}, db.NewSessionStore(nil), db.NewSessionThreadStore(nil), db.NewJobStore(nil))
	require.NoError(t, err, "construct explicit required test dependencies without acquiring a database connection")
	deps.Transactions = &unexpectedSendTransactions{t: t}
	return deps
}

func TestNewServiceRequiresSendDependencies(t *testing.T) {
	t.Parallel()
	var nilRunner *unexpectedSendTransactions
	var nilPersistence *sqlLegacySendPersistence
	var nilEffects *sqlSendEffects
	tests := []struct {
		name   string
		change func(*SendDependencies)
		want   string
	}{
		{"valid", func(*SendDependencies) {}, ""},
		{"zero", func(d *SendDependencies) { *d = SendDependencies{} }, "transactions"},
		{"nil runner", func(d *SendDependencies) { d.Transactions = nil }, "transactions"},
		{"typed nil runner", func(d *SendDependencies) { d.Transactions = nilRunner }, "transactions"},
		{"nil persistence", func(d *SendDependencies) { d.Legacy = nil }, "legacy persistence"},
		{"typed nil persistence", func(d *SendDependencies) { d.Legacy = nilPersistence }, "legacy persistence"},
		{"nil effects", func(d *SendDependencies) { d.Effects = nil }, "effects"},
		{"typed nil effects", func(d *SendDependencies) { d.Effects = nilEffects }, "effects"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deps := requiredTestSendDependencies(t)
			tt.change(&deps)
			service, err := NewService(nil, nil, nil, nil, nil, deps, zerolog.Nop())
			if tt.want != "" {
				require.EqualError(t, err, "construct thread service: thread send: "+tt.want+" is required", "missing send plumbing must fail during construction")
				require.Nil(t, service, "failed construction must expose no partially configured service")
				return
			}
			require.NoError(t, err, "explicit complete dependencies must construct without a transaction")
			require.Equal(t, deps, service.send, "the service must retain exactly the supplied mandatory dependencies")
			require.Nil(t, service.txStarter, "mandatory send dependencies must not enable the old combined-message transaction path")
		})
	}
}

func TestSendDependenciesValidateOwnedRunner(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, runner, expected string
	}{
		{name: "typed nil owned runner", runner: "nil", expected: "thread send: transactions is required"},
		{name: "zero owned runner", runner: "zero", expected: "thread send: invalid transactions: thread send transactions: pool is required"},
		{name: "configured owned runner", runner: "configured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deps := requiredTestSendDependencies(t)
			var runner *db.ThreadSendTransactions
			switch tt.runner {
			case "zero":
				runner = &db.ThreadSendTransactions{}
			case "configured":
				var err error
				runner, err = db.NewThreadSendTransactions(&pgxpool.Pool{})
				require.NoError(t, err, "construct an owned runner without opening a pool or querying the database")
			}
			deps.Transactions = runner
			err := deps.Validate()
			service, serviceErr := NewService(nil, nil, nil, nil, nil, deps, zerolog.Nop())
			if tt.expected != "" {
				require.EqualError(t, err, tt.expected, "invalid owned runners must fail dependency validation before routing or SQL")
				require.EqualError(t, serviceErr, "construct thread service: "+tt.expected, "service construction must preserve the invalid runner diagnosis")
				require.Nil(t, service, "an invalid owned runner must not escape in a partially configured service")
				return
			}
			require.NoError(t, err, "an owned runner with its concrete pool dependency may validate without a connection")
			require.NoError(t, serviceErr, "valid dependency construction must remain free of database work")
			require.Equal(t, deps, service.send, "the service must preserve the exact validated runner instead of substituting a fallback")
		})
	}
}
