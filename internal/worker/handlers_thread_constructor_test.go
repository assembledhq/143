package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type unexpectedWorkerSendTransactions struct{ t *testing.T }

func (r *unexpectedWorkerSendTransactions) Run(context.Context, func(context.Context, pgx.Tx) error) error {
	r.t.Helper()
	r.t.Fatal("constructor-only worker fixture unexpectedly invoked lifecycle transactions")
	return errors.New("unexpected lifecycle transactions")
}

func requiredWorkerThreadSendDependencies(t *testing.T) threadsvc.SendDependencies {
	t.Helper()
	deps, err := threadsvc.NewSendDependencies(&pgxpool.Pool{}, db.NewSessionStore(nil), db.NewSessionThreadStore(nil), db.NewJobStore(nil))
	require.NoError(t, err, "construct worker fixture dependencies without acquiring a database connection")
	deps.Transactions = &unexpectedWorkerSendTransactions{t: t}
	return deps
}

func TestNewWorkerThreadServiceRequiresSendDependencies(t *testing.T) {
	t.Parallel()
	var nilRunner *unexpectedWorkerSendTransactions
	tests := []struct{ name string }{{"nil stores"}, {"missing dependencies"}, {"typed nil runner"}, {"complete"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores := &Stores{ThreadSendDependencies: requiredWorkerThreadSendDependencies(t)}
			switch tt.name {
			case "nil stores":
				stores = nil
			case "missing dependencies":
				stores.ThreadSendDependencies = threadsvc.SendDependencies{}
			case "typed nil runner":
				stores.ThreadSendDependencies.Transactions = nilRunner
			}
			service, err := newWorkerThreadService(stores, zerolog.Nop())
			if tt.name == "complete" {
				require.NoError(t, err, "explicit dependencies must construct without starting a transaction")
				require.NotNil(t, service, "complete construction must return the thread service")
			} else {
				require.Error(t, err, "missing worker construction plumbing must be returned rather than replaced by a nil-backed service")
				require.Nil(t, service, "failed worker construction must not expose a partial service")
			}
		})
	}
}
