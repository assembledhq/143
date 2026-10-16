package db

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *JobStore) EnqueuePullRequestCheckSync(ctx context.Context, orgID, pullRequestID uuid.UUID, opts EnqueueOpts) (uuid.UUID, error) {
	return s.EnqueuePullRequestHealthWork(ctx, orgID, pullRequestID, opts)
}

// EnqueuePullRequestHealthWork preserves one pending successor for every
// health lane, including urgent head changes and database-only rebuilds.
func (s *JobStore) EnqueuePullRequestHealthWork(ctx context.Context, orgID, pullRequestID uuid.UUID, opts EnqueueOpts) (uuid.UUID, error) {
	payload, ok := opts.Payload.(map[string]string)
	if !ok || opts.DedupeKey == nil || opts.RunAt == nil || payload["pull_request_id"] != pullRequestID.String() {
		return uuid.Nil, fmt.Errorf("check sync requires matching pull request payload, dedupe key, and deadline")
	}
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return uuid.Nil, fmt.Errorf("job store does not support transactions")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("begin check sync enqueue: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM pull_requests
		WHERE org_id = @org_id AND id = @pull_request_id FOR UPDATE`, pgx.NamedArgs{
		"org_id": orgID, "pull_request_id": pullRequestID,
	}).Scan(&lockedID); err != nil {
		return uuid.Nil, fmt.Errorf("lock check sync pull request: %w", err)
	}

	rows, err := tx.Query(ctx, `SELECT j.id, j.status,
		EXISTS (SELECT 1 FROM jobs predecessor
			WHERE predecessor.org_id = @org_id
			  AND predecessor.id = NULLIF(j.payload->>'after_job_id', '')::uuid
			  AND predecessor.status IN ('pending', 'running')) AS awaiting_predecessor
		FROM jobs j
		WHERE j.org_id = @org_id AND j.queue = @queue AND j.job_type = @job_type
		  AND (j.dedupe_key = @dedupe_key OR left(j.dedupe_key, length(@successor_prefix)) = @successor_prefix)
		  AND j.status IN ('pending', 'running')
		ORDER BY j.created_at DESC, j.id DESC
		FOR UPDATE OF j`, pgx.NamedArgs{
		"org_id": orgID, "queue": opts.Queue, "job_type": opts.JobType,
		"dedupe_key": *opts.DedupeKey, "successor_prefix": *opts.DedupeKey + ":after:",
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("load active check sync lane: %w", err)
	}
	type activeCheckSync struct {
		ID                  uuid.UUID
		Status              models.JobStatus
		AwaitingPredecessor bool
	}
	active, err := pgx.CollectRows(rows, pgx.RowToStructByName[activeCheckSync])
	if err != nil {
		return uuid.Nil, fmt.Errorf("scan active check sync lane: %w", err)
	}
	for _, job := range active {
		if job.Status == models.JobStatusPending || job.AwaitingPredecessor {
			if err := tx.Commit(ctx); err != nil {
				return uuid.Nil, fmt.Errorf("commit absorbed check sync: %w", err)
			}
			return uuid.Nil, nil
		}
	}
	if len(active) > 0 {
		dedupeKey := *opts.DedupeKey + ":after:" + active[0].ID.String()
		opts.DedupeKey = &dedupeKey
		successorPayload := maps.Clone(payload)
		successorPayload["after_job_id"] = active[0].ID.String()
		opts.Payload = successorPayload
	}
	jobID, err := enqueueOn(ctx, tx, orgID, opts)
	if err != nil {
		return uuid.Nil, fmt.Errorf("enqueue check sync lane: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("commit check sync enqueue: %w", err)
	}
	s.notify(ctx, jobID)
	return jobID, nil
}

type PullRequestCheckSyncPredecessor struct {
	Status    models.JobStatus
	RunAt     time.Time
	CreatedAt time.Time
}

func (s *JobStore) GetPullRequestCheckSyncPredecessor(ctx context.Context, orgID, pullRequestID, predecessorID uuid.UUID, jobTypes ...string) (*PullRequestCheckSyncPredecessor, error) {
	jobType := "sync_pull_request_state"
	if len(jobTypes) > 0 {
		jobType = jobTypes[0]
	}
	if jobType != "sync_pull_request_state" && jobType != "rebuild_pull_request_health" {
		return nil, fmt.Errorf("invalid pull request health predecessor type")
	}
	var predecessor PullRequestCheckSyncPredecessor
	err := s.db.QueryRow(ctx, `SELECT status, run_at, created_at FROM jobs
		WHERE org_id = @org_id AND id = @predecessor_id
		  AND job_type = @job_type AND queue = 'default'
		  AND payload->>'pull_request_id' = @pull_request_id
		  AND status IN ('pending', 'running')`, pgx.NamedArgs{
		"org_id": orgID, "pull_request_id": pullRequestID.String(), "predecessor_id": predecessorID, "job_type": jobType,
	}).Scan(&predecessor.Status, &predecessor.RunAt, &predecessor.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load check sync predecessor: %w", err)
	}
	return &predecessor, nil
}
