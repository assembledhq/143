package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func outcomeReconciliationOnceOpts(orgID uuid.UUID, day string) EnqueueOpts {
	key := models.JobTypeReconcileCodeReviewOutcomes + ":" + orgID.String() + ":" + day
	return EnqueueOpts{
		Queue: "feedback", JobType: models.JobTypeReconcileCodeReviewOutcomes,
		Payload: map[string]any{"org_id": orgID.String(), "limit": 100}, Priority: 2, DedupeKey: &key,
	}
}

func TestOutcomeReconciliationOnceRejectsInvalidScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*EnqueueOpts, *uuid.UUID)
	}{
		{"missing tenant", func(_ *EnqueueOpts, id *uuid.UUID) { *id = uuid.Nil }},
		{"different queue", func(o *EnqueueOpts, _ *uuid.UUID) { o.Queue = "default" }},
		{"different type", func(o *EnqueueOpts, _ *uuid.UUID) { o.JobType = "reconcile_pull_request_state" }},
		{"missing key", func(o *EnqueueOpts, _ *uuid.UUID) { o.DedupeKey = nil }},
		{"empty key", func(o *EnqueueOpts, _ *uuid.UUID) { key := ""; o.DedupeKey = &key }},
		{"another tenant key", func(o *EnqueueOpts, _ *uuid.UUID) { *o = outcomeReconciliationOnceOpts(uuid.New(), "2026-04-23") }},
		{"different key prefix", func(o *EnqueueOpts, id *uuid.UUID) {
			key := "another_job:" + id.String() + ":2026-04-23"
			o.DedupeKey = &key
		}},
		{"invalid month", func(o *EnqueueOpts, id *uuid.UUID) { *o = outcomeReconciliationOnceOpts(*id, "2026-13-23") }},
		{"invalid day", func(o *EnqueueOpts, id *uuid.UUID) { *o = outcomeReconciliationOnceOpts(*id, "2026-02-29") }},
		{"unpadded date", func(o *EnqueueOpts, id *uuid.UUID) { *o = outcomeReconciliationOnceOpts(*id, "2026-4-23") }},
		{"timestamp instead of day", func(o *EnqueueOpts, id *uuid.UUID) { *o = outcomeReconciliationOnceOpts(*id, "2026-04-23T00:00:00Z") }},
		{"trailing data", func(o *EnqueueOpts, id *uuid.UUID) { *o = outcomeReconciliationOnceOpts(*id, "2026-04-23 ") }},
		{"empty day", func(o *EnqueueOpts, id *uuid.UUID) { *o = outcomeReconciliationOnceOpts(*id, "") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := uuid.New()
			opts := outcomeReconciliationOnceOpts(orgID, "2026-04-23")
			tt.mutate(&opts, &orgID)
			actual, err := NewJobStore(nil).EnqueueCodeReviewOutcomeReconciliationOnce(context.Background(), orgID, opts)
			require.Error(t, err, "invalid daily scope must fail before any database work")
			require.Equal(t, uuid.Nil, actual, "invalid scope must never report an admitted identity")
		})
	}
}

func TestOutcomeReconciliationOnceRequiresTopLevelTransaction(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	actual, err := NewJobStore(nil).EnqueueCodeReviewOutcomeReconciliationOnce(context.Background(), orgID, outcomeReconciliationOnceOpts(orgID, "2026-04-23"))
	require.ErrorContains(t, err, "does not support reconciliation admission transactions", "admission needs a top-level transaction rather than a nested savepoint")
	require.Equal(t, uuid.Nil, actual, "unsupported transaction ownership must not report durable admission")
}

func TestOutcomeReconciliationOncePreservesOptions(t *testing.T) {
	t.Parallel()
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing defaults", true: "explicit scheduling options"}[custom], func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated daily admission fixture")
			defer mock.Close()
			orgID, expectedID := uuid.New(), uuid.New()
			opts := outcomeReconciliationOnceOpts(orgID, "2024-02-29")
			payload, err := json.Marshal(opts.Payload)
			require.NoError(t, err, "encode exact unchanged daily payload")
			args := pgx.NamedArgs{"org_id": orgID, "queue": opts.Queue, "job_type": opts.JobType, "payload": payload, "priority": opts.Priority, "dedupe_key": opts.DedupeKey}
			if custom {
				node := "synthetic-worker"
				runAt := time.Date(2024, 2, 29, 9, 0, 0, 0, time.FixedZone("test", 3600))
				opts.TargetNodeID, opts.RunAt, opts.MaxAttempts = &node, &runAt, 7
				args["target_node_id"], args["run_at"], args["max_attempts"] = &node, runAt.UTC(), 7
			}
			mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(pgx.NamedArgs{"lock_key": "periodic-job:feedback:" + *opts.DedupeKey}).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery("SELECT EXISTS.*FROM jobs.*org_id = @org_id AND queue = @queue.*job_type = @job_type AND dedupe_key = @dedupe_key").WithArgs(pgx.NamedArgs{"org_id": orgID, "queue": opts.Queue, "job_type": opts.JobType, "dedupe_key": *opts.DedupeKey}).WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
			mock.ExpectQuery("INSERT INTO jobs").WithArgs(args).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(expectedID))
			mock.ExpectCommit()
			mock.ExpectRollback()
			actual, err := NewJobStore(mock).EnqueueCodeReviewOutcomeReconciliationOnce(context.Background(), orgID, opts)
			require.NoError(t, err, "a valid UTC date must admit the unchanged options")
			require.Equal(t, expectedID, actual, "daily admission must return the inserted job identity")
			require.NoError(t, mock.ExpectationsWereMet(), "daily admission must commit exactly once without rewriting retry or payload defaults")
		})
	}
}

func TestOutcomeReconciliationOncePropagatesTransactionErrors(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"begin", "lock", "lookup", "insert", "commit", "commit existing", "payload", "negative attempts"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated daily admission error fixture")
			defer mock.Close()
			orgID := uuid.New()
			opts := outcomeReconciliationOnceOpts(orgID, "2026-04-23")
			failure := errors.New("injected daily admission error")
			begin := mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			if stage == "begin" {
				begin.WillReturnError(failure)
			} else {
				lock := mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(pgx.NamedArgs{"lock_key": "periodic-job:feedback:" + *opts.DedupeKey})
				if stage == "lock" {
					lock.WillReturnError(failure)
				} else {
					lock.WillReturnResult(pgxmock.NewResult("SELECT", 1))
					lookup := mock.ExpectQuery("SELECT EXISTS.*FROM jobs.*org_id = @org_id AND queue = @queue.*job_type = @job_type AND dedupe_key = @dedupe_key").WithArgs(pgx.NamedArgs{"org_id": orgID, "queue": opts.Queue, "job_type": opts.JobType, "dedupe_key": *opts.DedupeKey})
					if stage == "lookup" {
						lookup.WillReturnError(failure)
					} else {
						lookup.WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(stage == "commit existing"))
						switch stage {
						case "payload":
							opts.Payload = func() {}
						case "negative attempts":
							opts.MaxAttempts = -1
						case "commit existing":
							mock.ExpectCommit().WillReturnError(failure)
						default:
							payload, err := json.Marshal(opts.Payload)
							require.NoError(t, err, "encode exact daily job payload")
							insert := mock.ExpectQuery("INSERT INTO jobs").WithArgs(pgx.NamedArgs{"org_id": orgID, "queue": opts.Queue, "job_type": opts.JobType, "payload": payload, "priority": opts.Priority, "dedupe_key": opts.DedupeKey})
							if stage == "insert" {
								insert.WillReturnError(failure)
							} else {
								insert.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
								mock.ExpectCommit().WillReturnError(failure)
							}
						}
					}
				}
				mock.ExpectRollback()
			}
			actual, err := NewJobStore(mock).EnqueueCodeReviewOutcomeReconciliationOnce(context.Background(), orgID, opts)
			if stage == "payload" || stage == "negative attempts" {
				require.Error(t, err, "invalid existing enqueue options must propagate without an insert")
			} else {
				require.ErrorIs(t, err, failure, "%s error must reach the producer", stage)
			}
			require.Equal(t, uuid.Nil, actual, "failed or ambiguous admission cannot claim a durable identity")
			require.NoError(t, mock.ExpectationsWereMet(), "failed admission must roll back without retrying the transaction")
		})
	}
}
