package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestReconciliationOnceRejectsInvalidScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*EnqueueOpts, *uuid.UUID)
	}{
		{"missing tenant", func(_ *EnqueueOpts, id *uuid.UUID) { *id = uuid.Nil }},
		{"different queue", func(o *EnqueueOpts, _ *uuid.UUID) { o.Queue = "agent" }},
		{"different type", func(o *EnqueueOpts, _ *uuid.UUID) { o.JobType = "sync_pull_request_state" }},
		{"missing key", func(o *EnqueueOpts, _ *uuid.UUID) { o.DedupeKey = nil }},
		{"another tenant key", func(o *EnqueueOpts, _ *uuid.UUID) { *o = reconciliationOnceOpts(uuid.New(), "20260423220") }},
		{"invalid date", func(o *EnqueueOpts, id *uuid.UUID) { *o = reconciliationOnceOpts(*id, "20261323220") }},
		{"invalid minute bucket", func(o *EnqueueOpts, id *uuid.UUID) { *o = reconciliationOnceOpts(*id, "20260423226") }},
		{"incomplete bucket", func(o *EnqueueOpts, id *uuid.UUID) { *o = reconciliationOnceOpts(*id, "2026042322") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id := uuid.New()
			opts := reconciliationOnceOpts(id, "20260423220")
			tt.mutate(&opts, &id)
			actual, err := NewJobStore(nil).EnqueuePullRequestReconciliationOnce(context.Background(), id, opts)
			require.Error(t, err, "invalid periodic scope must fail before any database work")
			require.Equal(t, uuid.Nil, actual, "rejected scope cannot claim durable admission")
		})
	}
}

func TestReconciliationOncePropagatesTransactionErrors(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"begin", "lock", "lookup", "insert", "commit"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated admission error fixture")
			defer mock.Close()
			orgID := uuid.New()
			opts := reconciliationOnceOpts(orgID, "20260423220")
			failure := errors.New("injected admission error")
			begin := mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			if stage == "begin" {
				begin.WillReturnError(failure)
			} else {
				lock := mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(pgx.NamedArgs{"lock_key": "periodic-job:default:" + *opts.DedupeKey})
				if stage == "lock" {
					lock.WillReturnError(failure)
				} else {
					lock.WillReturnResult(pgxmock.NewResult("SELECT", 1))
					lookup := mock.ExpectQuery("SELECT EXISTS.*FROM jobs.*org_id = @org_id.*job_type = @job_type.*dedupe_key = @dedupe_key").WithArgs(pgx.NamedArgs{"org_id": orgID, "queue": opts.Queue, "job_type": opts.JobType, "dedupe_key": *opts.DedupeKey})
					if stage == "lookup" {
						lookup.WillReturnError(failure)
					} else {
						lookup.WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
						payload, err := json.Marshal(opts.Payload)
						require.NoError(t, err, "encode exact expected job payload")
						insert := mock.ExpectQuery("INSERT INTO jobs").WithArgs(pgx.NamedArgs{"org_id": orgID, "queue": opts.Queue, "job_type": opts.JobType, "payload": payload, "priority": opts.Priority, "dedupe_key": opts.DedupeKey})
						if stage == "insert" {
							insert.WillReturnError(failure)
						} else {
							insert.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
							mock.ExpectCommit().WillReturnError(failure)
						}
					}
				}
				mock.ExpectRollback()
			}
			actual, err := NewJobStore(mock).EnqueuePullRequestReconciliationOnce(context.Background(), orgID, opts)
			require.ErrorIs(t, err, failure, "%s error must reach the caller", stage)
			require.Equal(t, uuid.Nil, actual, "failed admission cannot return an accepted identity")
			require.NoError(t, mock.ExpectationsWereMet(), "transaction must roll back on every failed admission stage")
		})
	}
}
