package deployguardrail

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrGuardrailSchemaUnavailable = errors.New("worker deploy guardrail schema unavailable")

type WorkerSessionCounts struct {
	WorkerOwnedActiveLongJobs    int64
	ExecutorOwnedActiveSessions  int64
	WorkerOwnedNoCheckpoint      int64
	DrainingSessionExecutorCount int64
}

type WorkerSessionDecision struct {
	Counts  WorkerSessionCounts
	Blocked bool
	Reason  string
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const workerSessionCountsQuery = `
	WITH active_session_jobs AS (
		SELECT
			j.id,
			j.owner_kind,
			COALESCE(s.snapshot_key, '') AS snapshot_key
		FROM jobs j
		LEFT JOIN sessions s
		  ON s.org_id = j.org_id
		 AND s.id = CASE
			WHEN COALESCE(j.payload->>'session_id', '') ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
			THEN (j.payload->>'session_id')::uuid
			ELSE NULL
		 END
		WHERE j.status = 'running'
		  AND j.job_type IN ('run_agent', 'continue_session')
	)
	SELECT
		COUNT(*) FILTER (WHERE owner_kind = 'worker') AS worker_owned_active_long_jobs,
		COUNT(*) FILTER (WHERE owner_kind = 'session_executor') AS executor_owned_active_sessions,
		COUNT(*) FILTER (WHERE owner_kind = 'worker' AND snapshot_key = '') AS worker_owned_no_checkpoint,
		(
			SELECT COUNT(*)
			FROM session_executors
			WHERE status = 'draining'
		) AS draining_session_executor_count
	FROM active_session_jobs`

func LoadWorkerSessionCounts(ctx context.Context, db querier) (WorkerSessionCounts, error) {
	var counts WorkerSessionCounts
	err := db.QueryRow(ctx, workerSessionCountsQuery).Scan(
		&counts.WorkerOwnedActiveLongJobs,
		&counts.ExecutorOwnedActiveSessions,
		&counts.WorkerOwnedNoCheckpoint,
		&counts.DrainingSessionExecutorCount,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.Code == "42P01" || pgErr.Code == "42703") {
			return WorkerSessionCounts{}, fmt.Errorf("%w: %v", ErrGuardrailSchemaUnavailable, err)
		}
		return WorkerSessionCounts{}, fmt.Errorf("load worker deploy session counts: %w", err)
	}
	return counts, nil
}

func IsDatabaseConnectionSaturated(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "53300"
}

func EvaluateWorkerSessionDeploy(counts WorkerSessionCounts, force bool) WorkerSessionDecision {
	decision := WorkerSessionDecision{Counts: counts}
	if counts.WorkerOwnedNoCheckpoint > 0 && !force {
		decision.Blocked = true
		decision.Reason = "active inline worker-owned session jobs without checkpoints"
	}
	return decision
}
